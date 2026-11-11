package workflows

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/workflow"
)

// Leave at least 1 MiB of the normal 2 MiB payload limit for other state.
// Count actual JSON bytes, including escaping and internal provenance.
const maxInboxJSONBytes = 1 << 20
const maxInboxCount = 64
const maxRetainedReceipts = 64

func runInputID(run int, requestID string) string {
	return fmt.Sprintf("run/%d/input/%s", run, requestID)
}

func updateMessage(ctx workflow.Context, msg MessageInput) MessageInput {
	if msg.RequestID == "" && ctx != nil {
		if info := workflow.GetCurrentUpdateInfo(ctx); info != nil {
			msg.RequestID = info.ID
		}
	}
	return msg
}
func (r *runtime) registerInboxHandlers(ctx workflow.Context) error {
	if err := workflow.SetUpdateHandlerWithOptions(ctx, MessageUpdate, r.message, workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context, msg MessageInput) error { return r.validateMessage(updateMessage(ctx, msg)) }}); err != nil {
		return err
	}
	if err := workflow.SetUpdateHandler(ctx, StopUpdate, r.stop); err != nil {
		return err
	}
	return workflow.SetUpdateHandler(ctx, ContinueUpdate, r.resume)
}
func messageDigest(msg MessageInput) string {
	b, _ := json.Marshal(msg)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (r *runtime) receipt(msg MessageInput) (MessageReceipt, bool, error) {
	if msg.RequestID != "" {
		for _, saved := range r.state.Receipts {
			if saved.RequestID == msg.RequestID {
				if saved.Digest != messageDigest(msg) {
					return MessageReceipt{}, true, fmt.Errorf("request ID reused with different message")
				}
				return saved.Receipt, true, nil
			}
		}
	}
	return MessageReceipt{}, false, nil
}
func (r *runtime) validateMessage(msg MessageInput) error {
	if msg.Internal || msg.OriginInputID != "" || msg.ContextOnly || msg.AfterRunCount != 0 {
		return fmt.Errorf("internal message metadata is not allowed")
	}
	if strings.TrimSpace(msg.Text) == "" {
		return fmt.Errorf("message must not be empty")
	}
	if len(msg.Text) > 64<<10 || len(msg.RequestID) > 256 {
		return fmt.Errorf("message or request ID too large")
	}
	switch msg.WhenBusy {
	case "", "followUp", "steer":
	default:
		return fmt.Errorf("invalid busy mode: use followUp or steer")
	}
	if _, found, err := r.receipt(msg); found {
		return err
	}
	return r.inboxFits(msg)
}
func (r *runtime) inboxFits(msg MessageInput) error {
	if len(r.state.Inbox) >= maxInboxCount {
		return fmt.Errorf("inbox is full")
	}
	size := 2
	for _, queued := range r.state.Inbox {
		b, err := json.Marshal(queued)
		if err != nil {
			return err
		}
		size += len(b) + 1
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if size+len(b) > maxInboxJSONBytes {
		return fmt.Errorf("inbox serialized size limit exceeded")
	}
	return nil
}
func (r *runtime) message(ctx workflow.Context, msg MessageInput) (MessageReceipt, error) {
	// No yields: Completed means durable enqueue, never model completion.
	msg = updateMessage(ctx, msg)
	if err := r.validateMessage(msg); err != nil {
		return MessageReceipt{}, err
	}
	if receipt, found, err := r.receipt(msg); found {
		return receipt, err
	}
	original := msg
	disposition := "queued"
	if r.state.Status == "idle" && len(r.state.Inbox) == 0 && !r.state.ContinueRequested {
		disposition = "started"
	} else if msg.WhenBusy == "steer" && !r.deferringSteers && r.state.Status != "idle" {
		disposition = "steered"
	}
	// A late steer is next-run work, never a request to reopen afterRun/settlement.
	if disposition != "steered" && msg.WhenBusy == "steer" {
		msg.WhenBusy = ""
	}
	r.state.Inbox = append(r.state.Inbox, msg)
	r.state.QueuePaused = false // Genuine new input resumes retained work, oldest first.
	receipt := MessageReceipt{Disposition: disposition}
	if msg.RequestID != "" {
		r.state.Receipts = append(r.state.Receipts, RetainedReceipt{RequestID: msg.RequestID, Digest: messageDigest(original), Receipt: receipt})
		if len(r.state.Receipts) > maxRetainedReceipts {
			r.state.Receipts = append([]RetainedReceipt(nil), r.state.Receipts[len(r.state.Receipts)-maxRetainedReceipts:]...)
		}
	}
	return receipt, nil
}
func (r *runtime) automaticWork() bool {
	return !r.state.QueuePaused && (r.state.ContinueRequested || len(r.state.Inbox) > 0)
}
func (r *runtime) nextInput(ctx workflow.Context) (MessageInput, error) {
	if err := workflow.Await(ctx, func() bool { return r.automaticWork() || r.shouldContinueAsNew(ctx) }); err != nil {
		return MessageInput{}, err
	}
	if err := r.continueAsNew(ctx); err != nil {
		return MessageInput{}, err
	}
	if r.state.ContinueRequested {
		r.state.ContinueRequested = false
		return MessageInput{Internal: true, ContextOnly: true, OriginInputID: r.state.OriginInputID, AfterRunCount: r.state.AfterRunCount}, nil
	}
	msg := r.state.Inbox[0]
	if msg.Internal {
		if err := validateRestartCount(msg.AfterRunCount); err != nil {
			return MessageInput{}, err
		}
	}
	r.state.Inbox = r.state.Inbox[1:]
	return msg, nil
}
func (r *runtime) hasSteers() bool {
	for _, msg := range r.state.Inbox {
		if msg.WhenBusy == "steer" {
			return true
		}
	}
	return false
}
func (r *runtime) drainSteers(ctx workflow.Context) error {
	for i := 0; i < len(r.state.Inbox); {
		msg := r.state.Inbox[i]
		if msg.WhenBusy != "steer" {
			i++
			continue
		}
		// Stable across activity retries; the message remains retained until durable.
		r.steerCounter++
		key := fmt.Sprintf("%s/steer/%d", r.inputID, r.steerCounter)
		if err := r.appendMessages(ctx, key, llm.Message{Role: "user", Content: msg.Text}); err != nil {
			return err
		}
		r.state.Inbox = append(r.state.Inbox[:i], r.state.Inbox[i+1:]...)
		r.state.HasUnansweredContext = true
		if !msg.Internal {
			r.state.AfterRunCount = 0
			r.state.OriginInputID = key
		}
	}
	return nil
}
func (r *runtime) shouldContinueAsNew(ctx workflow.Context) bool {
	limit := r.cfg.ContinueAfterTurns
	if limit <= 0 {
		limit = 100
	}
	for _, child := range r.state.Children {
		if child.Status == "working" || child.Status == "blocked" {
			return false
		}
	}
	return r.cancel == nil && (workflow.GetInfo(ctx).GetContinueAsNewSuggested() || r.state.TotalTurns-r.executionStartTurns >= limit)
}
func (r *runtime) continueAsNew(ctx workflow.Context) error {
	if !r.shouldContinueAsNew(ctx) {
		return nil
	}
	// shouldContinueAsNew excludes live children; the main loop only calls
	// this seam after run activity/child futures have joined.
	if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
		return err
	}
	r.pruneChildren()
	carry := r.state
	input := r.cfg
	input.Carry = &carry
	return workflow.NewContinueAsNewError(ctx, SessionWorkflow, input)
}
func (r *runtime) stop(workflow.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	// Idle Stop is a no-op. The run's failure boundary pauses retained inputs,
	// after cancellation has joined; do not turn a settled success into failure.
	return nil
}
func (r *runtime) resume(workflow.Context) error {
	if r.cancel != nil || r.state.Status != "idle" || r.automaticWork() {
		return fmt.Errorf("continue rejected: session is busy")
	}
	if !r.state.HasUnansweredContext {
		// An answered transcript with retained work resumes the FIFO directly,
		// never a context-only model turn over the previous final answer.
		if r.state.QueuePaused && len(r.state.Inbox) > 0 {
			r.state.QueuePaused = false
			return nil
		}
		err := fmt.Errorf("nothing to answer")
		r.fail("nothing_to_answer", err)
		return err
	}
	r.state.ContinueRequested = true
	r.state.QueuePaused = false
	return nil
}

const maxAfterRunCount = 2

func validateRestartCount(count int) error {
	if count < 0 || count > maxAfterRunCount {
		return fmt.Errorf("internal restart count must be between 0 and %d", maxAfterRunCount)
	}
	return nil
}

// queueInternalFollowUp is the PR17 seam, not an afterRun implementation.
// It snapshots the originating input and current, already-consumed restart count
// at enqueue time, not dequeue time. PR17 must check the limit and increment
// State.AfterRunCount BEFORE calling this helper; the helper never increments.
// On admission failure PR17 owns rollback. Genuine work ahead of this message
// may reset State's count; consuming this message restores its own snapshot.
func (r *runtime) queueInternalFollowUp(text string) error {
	if err := validateRestartCount(r.state.AfterRunCount); err != nil {
		return err
	}
	if r.state.OriginInputID == "" || len(r.state.OriginInputID) > 512 {
		return fmt.Errorf("invalid internal origin")
	}
	msg := MessageInput{Text: text, WhenBusy: "followUp", Internal: true, OriginInputID: r.state.OriginInputID, AfterRunCount: r.state.AfterRunCount}
	if len(text) > 64<<10 || strings.TrimSpace(text) == "" {
		return fmt.Errorf("invalid internal follow-up")
	}
	if err := r.inboxFits(msg); err != nil {
		return err
	}
	r.state.Inbox = append(r.state.Inbox, msg)
	return nil
}
