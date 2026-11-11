package activities

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

const chatSystemPrompt = "You are a helpful general chatbot. No tools or attachment access are available. History coverage is limited to recent completed text exchanges that fit; older exchanges are omitted, not summarized. Do not claim full conversation memory. Ask for missing context when needed."

// Count UTF-8 BYTES as tokens (conservative byte-tokenizer upper bound), plus
// 128 tokens per message for framing. All instructions are included. 12000 input
// + 2000 output + 2384 reserve <= 16384. Never pass oversized text to truncation.
func chatRequest(turn chat.Turn, recent []chat.Exchange) (llm.Request, error) {
	req := llm.Request{Model: turn.Model, ContextWindow: 16384, MaxOutputTokens: 2000, MaxResponseBytes: 65536}
	if !chat.ValidModel(turn.Model) || !utf8.ValidString(turn.Text) {
		return req, &ollama.InvalidRequestError{Err: errors.New("invalid text model input")}
	}
	used := len(chatSystemPrompt) + len(turn.Text) + 256
	if used > 12000 {
		return req, &ollama.InvalidRequestError{Err: errors.New("text input budget exceeded")}
	}
	var selected []chat.Exchange
	for _, e := range recent {
		cost := len(e.User) + len(e.Assistant) + 256
		if used+cost > 12000 {
			break
		}
		used += cost
		selected = append(selected, e)
		if len(selected) == 6 {
			break
		}
	}
	req.Messages = append(req.Messages, llm.Message{Role: "system", Content: chatSystemPrompt})
	for i := len(selected) - 1; i >= 0; i-- {
		req.Messages = append(req.Messages, llm.Message{Role: "user", Content: selected[i].User}, llm.Message{Role: "assistant", Content: selected[i].Assistant})
	}
	req.Messages = append(req.Messages, llm.Message{Role: "user", Content: turn.Text})
	return req, nil
}

// ChatModel stores the provider's complete response before reporting success;
// retries look up the same logical result before invoking the provider. No text
// or token stream is returned to Temporal. Final publication is a separate atomic
// settlement activity, so a DB outage can never masquerade as completion.
func (a *ChatActivities) ChatModel(ctx context.Context, ref chat.TurnRef) (retErr error) {
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
	history, err := a.Repository.RecentChatExchanges(ctx, ref)
	if err != nil {
		return err
	}
	req, err := chatRequest(turn, history)
	if err != nil {
		return err
	}
	attempt := 1
	if activity.IsActivity(ctx) {
		attempt = int(activity.GetInfo(ctx).Attempt)
	}
	attemptID := chat.LogicalID(ref.TurnID, fmt.Sprintf("model/0/attempt/%d", attempt))
	if err = a.Repository.BeginChatModelAttempt(ctx, chat.ModelAttempt{ID: attemptID, ConversationID: ref.ConversationID, TurnID: ref.TurnID, Attempt: attempt, Status: "running"}); err != nil {
		return err
	}
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
	if err == nil && (len(response.Message.ToolCalls) != 0 || len(response.Message.Content) > 65536 || !utf8.ValidString(response.Message.Content)) {
		err = &ollama.InvalidRequestError{Err: errors.New("unsupported model output")}
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
	return a.Repository.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: response.Message.Content, Tokens: response.Tokens, AttemptID: attemptID, LimitedHistory: true})
}
