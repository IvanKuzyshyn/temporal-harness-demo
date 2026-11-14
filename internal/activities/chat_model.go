package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	modelcontext "github.com/IvanKuzyshyn/temporal-harness-demo/internal/context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

const chatSystemPrompt = "You are a helpful general chatbot. No tools or attachment access are available. History coverage is limited to bounded completed exchanges and any explicitly supplied lossy summary. Do not claim full conversation memory. Ask for missing context when needed."

// Build mandatory current input without selecting history or budgeting against
// a different path's prompt. Web requests also reserve schemas and tool groups.
func mandatoryChatRequest(turn chat.Turn, prompt string) (llm.Request, error) {
	return modelcontext.Mandatory(turn, prompt)
}

// Shared source-bound rendered admission includes Unicode normalization, plus
// 128 tokens per message for framing. All instructions are included. 12000 input
// + 2000 output + 2384 reserve <= 16384. Never pass oversized text to truncation.
func chatRequest(turn chat.Turn, recent []chat.Exchange) (llm.Request, error) {
	req, err := mandatoryChatRequest(turn, chatSystemPrompt)
	if err != nil {
		return req, err
	}
	out, err := modelcontext.Assemble(req, modelcontext.Exchanges(recent), modelcontext.DefaultBudget())
	return out.Request, err
}

// ChatModel stores the provider's complete response before reporting success;
// retries look up the same logical result before invoking the provider. No text
// or token stream is returned to Temporal. Final publication is a separate atomic
// settlement activity, so a DB outage can never masquerade as completion.
func (a *ChatActivities) ChatModel(ctx context.Context, ref chat.TurnRef) (retErr error) {
	attempt := 1
	if activity.IsActivity(ctx) {
		attempt = int(activity.GetInfo(ctx).Attempt)
	}
	invocationID := uuid.NewString()
	if err := a.Repository.RegisterChatModelInvocation(ctx, ref, invocationID, attempt); err != nil {
		return chatStorageError(err) // No provider work without confirmed registration.
	}
	retErr = a.runChatModel(ctx, ref, attempt)
	// Only normal body return proves its provider/persistence and heartbeat/
	// shutdown goroutines joined. Do NOT defer this acknowledgment: panic or
	// Goexit must leave durable doubt, even though they also run Go defers.
	ack, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := a.Repository.AcknowledgeChatModelCleanup(ack, ref, invocationID); err != nil && retErr == nil {
		retErr = chatStorageError(err)
	}
	return retErr
}

func (a *ChatActivities) runChatModel(ctx context.Context, ref chat.TurnRef, attempt int) (retErr error) {
	activityCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	var stopping atomic.Bool
	joined := make(chan struct{})
	var workerStop <-chan struct{}
	if activity.IsActivity(activityCtx) {
		workerStop = activity.GetWorkerStopChannel(activityCtx)
	}
	go func() {
		defer close(joined)
		select {
		case <-workerStop:
			stopping.Store(true)
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-joined }()
	defer func() {
		var missing *ollama.ModelNotFoundError
		var invalid *ollama.InvalidRequestError
		switch {
		case stopping.Load():
			// Service suspension is retryable, never a user's cancellation.
			retErr = temporal.NewApplicationError("worker shutting down", "WorkerShutdown")
		case errors.Is(activityCtx.Err(), context.Canceled):
			retErr = temporal.NewCanceledError()
		case errors.As(retErr, &missing):
			retErr = temporal.NewNonRetryableApplicationError("configured model unavailable", "ModelNotFound", nil)
		case errors.As(retErr, &invalid):
			retErr = temporal.NewNonRetryableApplicationError(invalid.Error(), "InvalidRequest", nil)
		default:
			retErr = chatStorageError(retErr)
		}
	}()
	var tokens atomic.Int64
	stop := startStreamHeartbeats(ctx, func() {
		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, tokens.Load())
		}
	})
	defer stop()
	if _, err := a.Repository.GetChatModelResult(ctx, ref); err == nil {
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	turn, err := a.Repository.GetTurn(ctx, ref)
	if err != nil {
		return err
	}
	// Deterministic metadata-only preflight. No parser/original byte read and
	// no inference before these exact actions have recorded decisions.
	if a.WebTools != nil && ref.Step == 0 && len(turn.AttachmentIDs) > 0 {
		attemptID := chat.LogicalID(ref.TurnID, fmt.Sprintf("model/%d/attempt/%d", ref.Step, attempt))
		cached, e := a.Repository.BeginChatModelAttempt(ctx, chat.ModelAttempt{ID: attemptID, ConversationID: ref.ConversationID, TurnID: ref.TurnID, LogicalStep: ref.Step, Attempt: attempt, Status: "running"})
		if e != nil || cached {
			return e
		}
		rawCalls := make([]llm.ToolCall, 0, len(turn.AttachmentIDs))
		for _, id := range turn.AttachmentIDs {
			instruction := turn.Text
			if strings.TrimSpace(instruction) == "" {
				instruction = "Describe this attachment."
			}
			args, _ := json.Marshal(map[string]string{"attachmentId": string(id), "instruction": instruction})
			rawCalls = append(rawCalls, llm.ToolCall{Name: "file_upload", Args: args})
		}
		calls, e := a.webCalls(ctx, ref, rawCalls)
		if e != nil {
			return e
		}
		return a.Repository.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Calls: calls, AttemptID: attemptID, LimitedHistory: true})
	}
	attemptID := chat.LogicalID(ref.TurnID, fmt.Sprintf("model/%d/attempt/%d", ref.Step, attempt))
	cached, err := a.Repository.BeginChatModelAttempt(ctx, chat.ModelAttempt{ID: attemptID, ConversationID: ref.ConversationID, TurnID: ref.TurnID, LogicalStep: ref.Step, Attempt: attempt, Status: "running"})
	if err != nil {
		return err
	}
	if cached {
		return nil
	} // A winner may have committed after the fast cache read.
	var req llm.Request
	if a.WebTools != nil {
		req, err = a.webRequest(ctx, ref, turn, nil)
	} else {
		req, err = mandatoryChatRequest(turn, chatSystemPrompt)
	}
	if err != nil {
		return err
	}
	var assembled modelcontext.ModelContext
	if repository, ok := a.Repository.(modelcontext.Repository); ok {
		builder := modelcontext.Builder{Repository: repository, Model: a.Model, Mandatory: req, AttemptID: attemptID, Step: ref.Step}
		assembled, err = builder.Build(ctx, ref.ConversationID, ref.TurnID, modelcontext.DefaultBudget())
	} else {
		// Legacy injected repositories have only recent text. Production SQL
		// always implements the durable builder repository.
		var history []chat.Exchange
		history, err = a.Repository.RecentChatExchanges(ctx, ref)
		if err == nil {
			assembled, err = modelcontext.Assemble(req, modelcontext.Exchanges(history), modelcontext.DefaultBudget())
			assembled.Coverage.SourceLimited = true
		}
	}
	if err != nil {
		return err
	}
	req = assembled.Request
	if a.Model == nil {
		return errors.New("chat model not configured")
	}
	size := 0
	response, err := a.Model.Chat(ctx, req, func(d llm.Delta) error {
		tokens.Add(int64(d.Tokens))
		size += len(d.Text)
		if size > 65536 {
			return &ollama.InvalidRequestError{Err: errors.New("response byte budget exceeded")}
		}
		return ctx.Err()
	})
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && ((len(response.Message.ToolCalls) != 0 && a.WebTools == nil) || len(response.Message.ToolCalls) > 4 || len(response.Message.Content) > 65536 || !utf8.ValidString(response.Message.Content)) {
		err = &ollama.InvalidRequestError{Err: errors.New("unsupported model output")}
	}
	var calls []chat.WebModelCall
	if err == nil && len(response.Message.ToolCalls) > 0 {
		calls, err = a.webCalls(ctx, ref, response.Message.ToolCalls)
	}
	if err != nil {
		// WorkerStop arrives before the SDK's context deadline. Use that still
		// live, bounded context to settle the interrupted attempt, then join.
		if activityCtx.Err() == nil {
			finish, cancel := context.WithTimeout(activityCtx, 2*time.Second)
			reason := "model_error"
			if stopping.Load() {
				reason = "worker_shutdown"
			}
			persistErr := a.Repository.CompleteModelAttempt(finish, ref.ConversationID, attemptID, "failed", nil, "", reason)
			cancel()
			if persistErr != nil {
				return persistErr
			}
		}
		return err
	}
	assembled.Coverage.OutputTruncated = response.OutputExhausted(req.MaxOutputTokens)
	err = a.Repository.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Calls: calls, Text: response.Message.Content, Tokens: response.Tokens, AttemptID: attemptID, LimitedHistory: true, Context: &chat.ContextMetadata{Coverage: assembled.Coverage, Accounting: assembled.Accounting}})
	if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrStaleChatAttempt) {
		// Never overwrite a canonical response. A different attempt's committed
		// winner is reusable; changed output from THIS owner is still a defect.
		winner, readErr := a.Repository.GetChatModelResult(ctx, ref)
		if readErr == nil && winner.AttemptID != "" && winner.AttemptID != attemptID {
			return nil
		}
		if readErr != nil && !errors.Is(readErr, storage.ErrNotFound) {
			return readErr
		}
	}
	return err
}
