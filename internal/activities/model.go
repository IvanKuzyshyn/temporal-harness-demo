package activities

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// LLMStream streams display events, but persists the complete response before
// returning. Retries reuse that response rather than appending another assistant
// message. Failed attempts restart the turn, never resume partial model state.
func (a *Activities) LLMStream(ctx context.Context, in workflows.LLMInput) (result workflows.LLMResult, retErr error) {
	defer func() {
		if retErr == nil {
			return
		}
		var missing *ollama.ModelNotFoundError
		var invalid *ollama.InvalidRequestError
		switch {
		case errors.Is(retErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			retErr = temporal.NewCanceledError()
		case errors.As(retErr, &missing):
			retErr = temporal.NewNonRetryableApplicationError(retErr.Error(), "ModelNotFound", retErr)
		case errors.As(retErr, &invalid):
			retErr = temporal.NewNonRetryableApplicationError(retErr.Error(), "InvalidRequest", retErr)
		}
	}()
	var zero workflows.LLMResult
	if in.TranscriptRef != TranscriptRef(in.Scope.WorkflowID) {
		return zero, fmt.Errorf("transcript identity mismatch")
	}
	key, err := activityKey(ctx, in.InputID)
	if err != nil {
		return zero, err
	}
	id := entryKey(in.Scope, fmt.Sprintf("%s/turn/%d/assistant", key, in.Turn))
	path, err := transcriptPath(in.DataDir, in.TranscriptRef)
	if err != nil {
		return zero, err
	}
	entries, err := readEntries(path)
	if err != nil {
		return zero, err
	}
	var saved *transcriptEntry
	var messages []llm.Message
	for i := range entries {
		if entries[i].ID == id {
			saved = &entries[i]
			break
		}
		messages = append(messages, entries[i].Message)
	}
	var seq uint64
	var displayMu sync.Mutex
	emit := func(typ events.Type, data any) error {
		displayMu.Lock()
		defer displayMu.Unlock()
		pattern := events.None
		if typ == events.MessageUpdate || typ == events.Heartbeat {
			pattern = events.LongRunning
		}
		return a.emitDisplay(ctx, workflows.EmitInput{Scope: in.Scope, DataDir: in.DataDir, Type: typ, Pattern: pattern, Data: data}, &seq)
	}
	ref := in.TranscriptRef + "#" + id
	if saved == nil {
		if a.Model == nil {
			return zero, fmt.Errorf("model is not configured")
		}
		if err = emit(events.MessageStart, events.MessageData{Role: "assistant", MessageRef: ref}); err != nil {
			return zero, err
		}
		partial, partialRef, err := openPartial(ctx, in, id)
		if err != nil {
			return zero, err
		}
		defer func() {
			syncErr := partial.Sync()
			closeErr := partial.Close()
			if retErr == nil {
				retErr = errors.Join(syncErr, closeErr)
			}
		}()
		var tokens atomic.Int64
		stop := startStreamHeartbeats(ctx, func() {
			progress := streamProgress{Tokens: int(tokens.Load()), PartialRef: partialRef}
			if activity.IsActivity(ctx) {
				activity.RecordHeartbeat(ctx, progress)
			}
			_ = emit(events.Heartbeat, progress)
		})
		defer stop()
		res, err := a.Model.Chat(ctx, llm.Request{Model: in.Model, Messages: messages, Tools: in.Tools, Scenario: in.Scenario, Turn: in.Turn}, func(d llm.Delta) error {
			if _, err := partial.WriteString(d.Text); err != nil {
				return err
			}
			tokens.Add(int64(d.Tokens))
			// Chunk unusually large provider deltas to stay below event record bounds.
			for len(d.Text) > MaxSummaryBytes {
				part := bounded(d.Text, MaxSummaryBytes)
				if err := emit(events.MessageUpdate, events.MessageData{Role: "assistant", MessageRef: ref, Delta: part}); err != nil {
					return err
				}
				d.Text = d.Text[len(part):]
			}
			return emit(events.MessageUpdate, events.MessageData{Role: "assistant", MessageRef: ref, Delta: d.Text, Tokens: d.Tokens})
		})
		if err != nil {
			return zero, err
		}
		if err = ctx.Err(); err != nil {
			return zero, err
		}
		stop()
		if err := partial.Sync(); err != nil {
			return zero, err
		}
		if len(res.Message.ToolCalls) > 32 {
			return zero, fmt.Errorf("model tool-call limit exceeded")
		}
		totalArgs := 0
		for _, call := range res.Message.ToolCalls {
			totalArgs += len(call.Args) + len(call.ID) + len(call.Name)
		}
		if totalArgs > 64<<10 {
			return zero, fmt.Errorf("model tool-call payload limit exceeded")
		}
		res.Message.Role = "assistant"
		candidate := transcriptEntry{ID: id, Message: res.Message, Tokens: res.Tokens}
		err = mutateTranscript(ctx, in.DataDir, in.TranscriptRef, func(entries *[]transcriptEntry) error {
			for _, e := range *entries {
				if e.ID == id {
					saved = &e
					return nil
				}
			}
			*entries = append(*entries, candidate)
			saved = &candidate
			return nil
		})
		if err != nil {
			return zero, err
		}
	}
	err = emit(events.MessageEnd, events.MessageData{Role: "assistant", MessageRef: ref, Tokens: saved.Tokens})
	if err != nil {
		return zero, err
	}
	return workflows.LLMResult{MessageRef: ref, Summary: bounded(saved.Message.Content, MaxSummaryBytes), ToolCalls: saved.Message.ToolCalls, Tokens: saved.Tokens, LastSeq: seq}, nil
}
