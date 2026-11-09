package workflows

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.temporal.io/sdk/workflow"
)

// Leave at least 1 MiB of the normal 2 MiB payload limit for the rest of
// State and converter metadata. Count actual JSON bytes, including escaping.
const maxInboxJSONBytes = 1 << 20

func runInputID(run int, requestID string) string {
	return fmt.Sprintf("run/%d/input/%s", run, requestID)
}

func (r *runtime) registerInboxHandlers(ctx workflow.Context) error {
	if err := workflow.SetUpdateHandlerWithOptions(ctx, MessageUpdate, r.message, workflow.UpdateHandlerOptions{Validator: r.validateMessage}); err != nil {
		return err
	}
	if err := workflow.SetUpdateHandler(ctx, StopUpdate, r.stop); err != nil {
		return err
	}
	return workflow.SetUpdateHandler(ctx, ContinueUpdate, r.resume)
}
func (r *runtime) validateMessage(msg MessageInput) error {
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
	if len(r.state.Inbox) >= 64 {
		return fmt.Errorf("inbox is full")
	}
	size := 2 // array delimiters
	for _, queued := range r.state.Inbox {
		b, err := json.Marshal(queued)
		if err != nil {
			return err
		}
		size += len(b) + 1 // comma before the candidate or next entry
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
func (r *runtime) message(_ workflow.Context, msg MessageInput) (MessageReceipt, error) {
	// No yields, activities or model calls: Completed means durable enqueue only.
	disposition := "queued"
	if r.state.Status == "idle" && len(r.state.Inbox) == 0 {
		disposition = "started"
	} else if msg.WhenBusy == "steer" {
		disposition = "steered"
	}
	r.state.Inbox = append(r.state.Inbox, msg)
	return MessageReceipt{Disposition: disposition}, nil
}
func (r *runtime) nextInput(ctx workflow.Context) (MessageInput, error) {
	if err := workflow.Await(ctx, func() bool { return len(r.state.Inbox) > 0 }); err != nil {
		return MessageInput{}, err
	}
	msg := r.state.Inbox[0]
	r.state.Inbox = r.state.Inbox[1:]
	return msg, nil
}

// Basic sessions preserve all inputs as FIFO follow-ups. The inbox/failures unit
// replaces this seam to drain steer messages at safe turn boundaries.
func (r *runtime) drainSteers(workflow.Context) error   { return nil }
func (r *runtime) continueAsNew(workflow.Context) error { return nil }
func (r *runtime) stop(workflow.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}
func (r *runtime) resume(workflow.Context) error {
	return fmt.Errorf("continue is not supported in the basic session")
}
