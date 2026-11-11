package workflows

import (
	"errors"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WebSessionName          = "WebChatSessionWorkflow"
	WebAgentsQueue          = "web-chat-agents"
	WebLLMQueue             = "web-chat-llm"
	WebDocumentsQueue       = "web-chat-documents" // reserved; no child worker before Task8
	WebCheckCommandActivity = "CheckChatCommand"
	WebAdmissionActivity    = "RecordChatAdmission"
	WebStartTurnActivity    = "StartChatTurn"
	WebModelActivity        = "ChatModel"
	WebPersistTurnActivity  = "PersistTurnResult"
)

type WebSessionInput struct {
	ConversationID          chat.ConversationID
	Model                   string
	MaxInbox, ContinueAfter int
	Carry                   *WebSessionState
}
type WebSessionState struct {
	Status              string
	ActiveTurn          chat.TurnID
	Inbox               []chat.TurnRef
	LastMessageSequence int64
	Completed, Failed   int64
}

func webStorageContext(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: WebAgentsQueue, StartToCloseTimeout: 10 * time.Second, ScheduleToCloseTimeout: 2 * time.Minute, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second, MaximumAttempts: 5}})
}

// WebChatSessionWorkflow is the only text-chat execution loop. It never loads
// transcripts or makes DB/network calls; all carry is bounded metadata/IDs.
func WebChatSessionWorkflow(ctx workflow.Context, in WebSessionInput) error {
	if in.Model == "" {
		in.Model = "qwen3.5:9b"
	}
	if !chat.ValidID(string(in.ConversationID)) || !chat.ValidModel(in.Model) {
		return temporal.NewNonRetryableApplicationError("invalid session", "InvalidRequest", nil)
	}
	if in.MaxInbox <= 0 || in.MaxInbox > 64 {
		in.MaxInbox = 64
	}
	if in.ContinueAfter <= 0 || in.ContinueAfter > 100 {
		in.ContinueAfter = 100
	}
	state := WebSessionState{Status: "idle"}
	if in.Carry != nil {
		state = *in.Carry
	}
	in.Carry = nil
	if len(state.Inbox) > in.MaxInbox || state.ActiveTurn != "" {
		return temporal.NewNonRetryableApplicationError("invalid carry", "InvalidRequest", nil)
	}
	if err := workflow.SetQueryHandler(ctx, "state", func() (WebSessionState, error) { return state, nil }); err != nil {
		return err
	}
	mutex := workflow.NewMutex(ctx)
	operations := 0
	var admissionErr error
	handler := func(uctx workflow.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
		reject := chat.AdmissionResult{State: "rejected", Code: "invalid_command"}
		// Count before the first yield, including handlers waiting on the mutex.
		operations++
		if err := mutex.Lock(uctx); err != nil {
			return chat.AdmissionResult{}, err
		}
		defer mutex.Unlock()
		hash, err := (chat.CommandInput{ConversationID: c.ConversationID, RequestID: c.RequestID, Kind: c.Kind, Payload: c.Payload}).Digest()
		if err != nil || hash != c.PayloadDigest || c.ConversationID != in.ConversationID || c.Model != in.Model || c.WorkflowID != "web-chat/"+string(in.ConversationID) || !chat.ValidID(string(c.CommandID)) || c.Sequence <= 0 {
			return reject, nil
		}
		if info := workflow.GetCurrentUpdateInfo(uctx); info != nil && info.ID != string(c.CommandID) {
			return reject, nil
		}
		var saved chat.AdmissionResult
		actx := webStorageContext(uctx)
		if err = workflow.ExecuteActivity(actx, WebCheckCommandActivity, c).Get(uctx, &saved); err != nil {
			admissionErr = err
			return chat.AdmissionResult{}, err
		}
		if saved.State != "" {
			return saved, nil
		}
		result := reject
		if c.Kind == chat.CommandMessage && len(c.Payload.AttachmentIDs) == 0 && c.Payload.Text != "" && len(c.Payload.Text) <= 65536 && chat.ValidID(string(c.TurnID)) {
			// Only the durable receipt can certify old-command acceptance. An
			// unseen older sequence is a broken FIFO submission, not a retry.
			if c.Sequence > state.LastMessageSequence && len(state.Inbox) < in.MaxInbox {
				// No yield between append and watermark. The admission activity can only
				// run once this enqueue's workflow task is durably committed. Crash before
				// receipt: replay enqueues; crash after receipt: replay or bounded CAN carry
				// retains the pending turn. The DB marker alone never precedes enqueue.
				state.Inbox = append(state.Inbox, chat.TurnRef{ConversationID: in.ConversationID, TurnID: c.TurnID})
				state.LastMessageSequence = c.Sequence
				result = chat.AdmissionResult{State: "accepted"}
			}
		}
		if err = workflow.ExecuteActivity(actx, WebAdmissionActivity, chat.Admission{Command: c, Result: result}).Get(uctx, nil); err != nil {
			admissionErr = err
			return chat.AdmissionResult{}, err
		}
		return result, nil
	}
	for _, name := range []string{"message", "stop", "replace", "decide", "revoke"} {
		name := name
		if err := workflow.SetUpdateHandlerWithOptions(ctx, name, func(u workflow.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
			if string(c.Kind) != name {
				return chat.AdmissionResult{State: "rejected", Code: "invalid_command"}, nil
			}
			return handler(u, c)
		}, workflow.UpdateHandlerOptions{Validator: func(workflow.Context, chat.SessionCommand) error {
			// A validator rejection is not a completed Update result: the same ID
			// can be retried after CAN. Do not cache a pending result forever or
			// keep adding history while a long inference prevents safe rollover.
			if operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
				return errors.New("session rollover pending; retry command")
			}
			return nil
		}}); err != nil {
			return err
		}
	}
	for {
		if err := workflow.Await(ctx, func() bool {
			return len(state.Inbox) > 0 || admissionErr != nil || operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested()
		}); err != nil {
			return err
		}
		if admissionErr != nil {
			return admissionErr
		}
		if operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			// No children exist in this slice; the main loop has joined all activities.
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return err
			}
			if admissionErr != nil {
				return admissionErr
			}
			in.Carry = &state
			return workflow.NewContinueAsNewError(ctx, WebSessionName, in)
		}
		ref := state.Inbox[0]
		state.Inbox = state.Inbox[1:]
		state.ActiveTurn = ref.TurnID
		state.Status = "running"
		if err := workflow.ExecuteActivity(webStorageContext(ctx), WebStartTurnActivity, ref).Get(ctx, nil); err != nil {
			return err
		}
		mctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: WebLLMQueue, StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 25 * time.Minute, HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Second, NonRetryableErrorTypes: []string{"InvalidRequest", "ModelNotFound"}}})
		// Shutdown is not workflow cancellation. A genuine cancellation joins the
		// activity before return; stop/replace control semantics belong to Task5.
		join, _ := workflow.NewDisconnectedContext(ctx)
		err := workflow.ExecuteActivity(mctx, WebModelActivity, ref).Get(join, nil)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result := chat.TurnResult{TurnRef: ref, Status: "completed"}
		if err != nil {
			result.Status = "failed"
			result.Reason = "model_error"
		}
		state.Status = "persisting"
		if err = workflow.ExecuteActivity(webStorageContext(ctx), WebPersistTurnActivity, result).Get(ctx, nil); err != nil {
			state.Status = "persistence_failed"
			return err
		}
		if result.Status == "completed" {
			state.Completed++
		} else {
			state.Failed++
		}
		state.ActiveTurn = ""
		state.Status = "idle"
	}
}
