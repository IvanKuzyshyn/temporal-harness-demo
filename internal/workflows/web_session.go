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
	// Recovery carries only an active ID, phase and settlement metadata, never
	// response text. CAN can checkpoint between failed persistence retry batches.
	RecoveryPhase        string
	PendingResult        *chat.TurnResult
	RecoveryDelay        time.Duration
	RecoveringAdmissions int
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
	validCheckpoint := state.ActiveTurn == "" && state.RecoveryPhase == "" && state.PendingResult == nil
	if state.ActiveTurn != "" && state.Status == "recovering" && chat.ValidID(string(state.ActiveTurn)) {
		validCheckpoint = state.RecoveryPhase == "start" && state.PendingResult == nil
		if r := state.PendingResult; r != nil {
			validCheckpoint = state.RecoveryPhase == "settle" && r.TurnID == state.ActiveTurn && r.ConversationID == in.ConversationID && (r.Status == "completed" || r.Status == "failed")
		}
	}
	if len(state.Inbox) > in.MaxInbox || !validCheckpoint || state.RecoveringAdmissions != 0 || state.RecoveryDelay < 0 || state.RecoveryDelay > time.Minute {
		return temporal.NewNonRetryableApplicationError("invalid carry", "InvalidRequest", nil)
	}
	if err := workflow.SetQueryHandler(ctx, "state", func() (WebSessionState, error) { return state, nil }); err != nil {
		return err
	}
	mutex := workflow.NewMutex(ctx)
	operations := 0
	var admissionErr error
	// An accepted Update must not finish with a transient storage error: that
	// outcome would be cached forever under its stable ID. Retain the replayable
	// handler and backpressure new admissions while retrying bounded batches.
	admissionStorage := func(u workflow.Context, name string, arg, out any) error {
		recovering := false
		defer func() {
			if recovering {
				state.RecoveringAdmissions--
			}
		}()
		delay := time.Duration(0)
		for {
			err := workflow.ExecuteActivity(webStorageContext(u), name, arg).Get(u, out)
			if err == nil || u.Err() != nil || !retryWebStorage(err) {
				return err
			}
			if !recovering {
				state.RecoveringAdmissions++
				recovering = true
			}
			operations++
			delay = webRecoveryDelay(delay)
			if err = workflow.Sleep(u, delay); err != nil {
				return err
			}
		}
	}
	handler := func(u workflow.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
		reject := chat.AdmissionResult{State: "rejected", Code: "invalid_command"}
		operations++ // Count before the first yield, including mutex waiters.
		if err := mutex.Lock(u); err != nil {
			return chat.AdmissionResult{}, err
		}
		defer mutex.Unlock()
		hash, err := (chat.CommandInput{ConversationID: c.ConversationID, RequestID: c.RequestID, Kind: c.Kind, Payload: c.Payload}).Digest()
		if err != nil || hash != c.PayloadDigest || c.ConversationID != in.ConversationID || c.Model != in.Model || c.WorkflowID != "web-chat/"+string(in.ConversationID) || !chat.ValidID(string(c.CommandID)) || c.Sequence <= 0 {
			return reject, nil
		}
		if info := workflow.GetCurrentUpdateInfo(u); info != nil && info.ID != string(c.CommandID) {
			return reject, nil
		}
		var saved chat.AdmissionResult
		if err = admissionStorage(u, WebCheckCommandActivity, c, &saved); err != nil {
			admissionErr = err
			return chat.AdmissionResult{}, err
		}
		if saved.State != "" {
			return saved, nil
		}
		result := reject
		if c.Kind == chat.CommandMessage && len(c.Payload.AttachmentIDs) == 0 && c.Payload.Text != "" && len(c.Payload.Text) <= 65536 && chat.ValidID(string(c.TurnID)) {
			// Only a durable receipt certifies old acceptance; the sequence is a fence.
			if c.Sequence > state.LastMessageSequence && len(state.Inbox) < in.MaxInbox {
				// No yield between enqueue and watermark. Admission persistence is only
				// scheduled AFTER enqueue's workflow task can be durably replayed.
				state.Inbox = append(state.Inbox, chat.TurnRef{ConversationID: in.ConversationID, TurnID: c.TurnID})
				state.LastMessageSequence = c.Sequence
				result = chat.AdmissionResult{State: "accepted"}
			}
		}
		if err = admissionStorage(u, WebAdmissionActivity, chat.Admission{Command: c, Result: result}, nil); err != nil {
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
			// Rejection BEFORE admission is retryable with the same ID; never return a
			// completed pending outcome or abandon an admitted handler on rollover.
			if operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested() || state.Status == "recovering" || state.RecoveringAdmissions > 0 {
				return errors.New("session recovery or rollover pending; retry command")
			}
			return nil
		}}); err != nil {
			return err
		}
	}
	recoverTurn := func(err error) error {
		if ctx.Err() != nil || !retryWebStorage(err) {
			state.Status = "persistence_failed"
			return err
		}
		state.Status = "recovering"
		operations++
		state.RecoveryDelay = webRecoveryDelay(state.RecoveryDelay)
		return workflow.Sleep(ctx, state.RecoveryDelay)
	}
	for {
		if err := workflow.Await(ctx, func() bool {
			return state.ActiveTurn != "" || len(state.Inbox) > 0 || admissionErr != nil || operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested()
		}); err != nil {
			return err
		}
		if admissionErr != nil {
			return admissionErr
		}
		if operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			// Every activity is joined here. A recovering turn carries its checkpoint,
			// not a live activity/child. Pending handlers must still finish first.
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return err
			}
			if admissionErr != nil {
				return admissionErr
			}
			in.Carry = &state
			return workflow.NewContinueAsNewError(ctx, WebSessionName, in)
		}
		if state.ActiveTurn == "" {
			state.ActiveTurn = state.Inbox[0].TurnID
			state.Inbox = state.Inbox[1:]
			state.RecoveryPhase = "start"
		}
		ref := chat.TurnRef{ConversationID: in.ConversationID, TurnID: state.ActiveTurn}
		if state.RecoveryPhase == "start" {
			state.Status = "running"
			if err := workflow.ExecuteActivity(webStorageContext(ctx), WebStartTurnActivity, ref).Get(ctx, nil); err != nil {
				if err = recoverTurn(err); err != nil {
					return err
				}
				continue
			}
			state.RecoveryDelay = 0
			mctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: WebLLMQueue, StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 25 * time.Minute, HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Second, NonRetryableErrorTypes: []string{"InvalidRequest", "ModelNotFound"}}})
			// Service shutdown is not user cancellation; genuine cancellation joins.
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
			state.PendingResult = &result
			state.RecoveryPhase = "settle"
		}
		state.Status = "persisting"
		if err := workflow.ExecuteActivity(webStorageContext(ctx), WebPersistTurnActivity, *state.PendingResult).Get(ctx, nil); err != nil {
			if err = recoverTurn(err); err != nil {
				return err
			}
			continue
		}
		if state.PendingResult.Status == "completed" {
			state.Completed++
		} else {
			state.Failed++
		}
		state.ActiveTurn = ""
		state.PendingResult = nil
		state.RecoveryPhase = ""
		state.RecoveryDelay = 0
		state.Status = "idle"
	}
}
