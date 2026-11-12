package workflows

import (
	"errors"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WebSessionName             = "WebChatSessionWorkflow"
	WebAgentsQueue             = "web-chat-agents"
	WebLLMQueue                = "web-chat-llm"
	WebDocumentsQueue          = "web-chat-documents" // reserved; child execution belongs to Task9
	WebCheckCommandActivity    = "CheckChatCommand"
	WebAdmissionActivity       = "RecordChatAdmission"
	WebResumeAdmissionActivity = "ResumeChatAdmission"
	WebStartTurnActivity       = "StartChatTurn"
	WebModelActivity           = "ChatModel"
	WebSealModelActivity       = "SealChatModelInvocations"
	WebPersistTurnActivity     = "PersistTurnResult"
)

type WebSessionInput struct {
	ConversationID          chat.ConversationID
	Model                   string
	MaxInbox, ContinueAfter int
	Carry                   *WebSessionState
}
type WebSessionState struct {
	Status                      string
	ActiveTurn                  chat.TurnID
	Inbox                       []chat.TurnRef
	LastMessageSequence         int64
	Completed, Failed           int64
	Cancelled                   int64
	UnconfirmedModelInvocations int64
	Control                     *WebControl
	// Recovery carries only an active ID, phase and settlement metadata, never
	// response text. CAN can checkpoint between failed persistence retry batches.
	RecoveryPhase        string
	PendingResult        *chat.TurnResult
	RecoveryDelay        time.Duration
	RecoveringAdmissions int
	PendingAdmissions    []chat.AdmissionCheckpoint
	// At most one serialized receipt write is in flight. Query evidence for
	// terminal reconciliation; handlers drain/handoff before CAN.
	DecidingAdmission *chat.AdmissionCheckpoint
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
			validCheckpoint = (state.RecoveryPhase == "join" || state.RecoveryPhase == "settle") && r.TurnID == state.ActiveTurn && r.ConversationID == in.ConversationID && (r.Status == "completed" || r.Status == "failed")
			if state.Control != nil && r.Status == "cancelled" && r.TurnID == state.ActiveTurn && r.ConversationID == in.ConversationID {
				validCheckpoint = state.RecoveryPhase == "cancel_mark" || state.RecoveryPhase == "cancel_join" || state.RecoveryPhase == "cancel_settle"
			}
		}
	}
	for _, cp := range state.PendingAdmissions {
		c := cp.Command
		resultValid := cp.Phase == "check" && cp.Result == (chat.AdmissionResult{}) || cp.Phase == "receipt" && validWebReceipt(cp.Result)
		if !validWebIdentity(c, in.ConversationID, in.Model) || !resultValid || (cp.Eligible && c.Kind != chat.CommandStop && !chat.ValidID(string(c.TurnID))) {
			validCheckpoint = false
		}
	}
	if control := state.Control; control != nil {
		c := control.Command
		if !validWebIdentity(c, in.ConversationID, in.Model) || c.ExpectedActiveTurnID != state.ActiveTurn || !chat.ValidID(string(c.ExpectedActiveTurnID)) || (c.Kind != chat.CommandStop && c.Kind != chat.CommandReplace) || (c.Kind == chat.CommandReplace && !chat.ValidID(string(c.TurnID))) || (c.Kind == chat.CommandStop && c.TurnID != "") {
			validCheckpoint = false
		}
	}
	for _, ref := range state.Inbox {
		if ref.ConversationID != in.ConversationID || !chat.ValidID(string(ref.TurnID)) {
			validCheckpoint = false
		}
	}
	if r := state.PendingResult; r != nil && len(r.Reason) > 128 {
		validCheckpoint = false
	}
	if state.UnconfirmedModelInvocations != 0 || state.DecidingAdmission != nil || len(state.Inbox) > in.MaxInbox || len(state.PendingAdmissions) > 101 || !validCheckpoint || state.RecoveringAdmissions != len(state.PendingAdmissions) || state.RecoveryDelay < 0 || state.RecoveryDelay > time.Minute {
		return temporal.NewNonRetryableApplicationError("invalid carry", "InvalidRequest", nil)
	}
	if err := workflow.SetQueryHandler(ctx, "state", func() (WebSessionState, error) { return state, nil }); err != nil {
		return err
	}
	mutex := workflow.NewMutex(ctx)
	operations := 0
	var admissionErr error
	var turnCancel workflow.CancelFunc
	var turnCtx workflow.Context
	join, _ := workflow.NewDisconnectedContext(ctx)
	// A handler performs at most one bounded batch per storage phase. On
	// exhaustion it records a reference-only handoff BEFORE completing pending,
	// and forces CAN. The same Update ID can then obtain its durable outcome in
	// the new run; no transient failure is permanently cached or handler abandoned.
	handoff := func(c chat.SessionCommand, phase string, eligible bool, result chat.AdmissionResult) (chat.AdmissionResult, error) {
		state.PendingAdmissions = append(state.PendingAdmissions, chat.AdmissionCheckpoint{Command: c.Identity(), Phase: phase, Eligible: eligible, Result: result})
		state.RecoveringAdmissions = len(state.PendingAdmissions)
		operations = in.ContinueAfter
		return chat.AdmissionResult{State: "pending"}, nil
	}
	admit := func(c chat.CommandIdentity, eligible bool) chat.AdmissionResult {
		result := chat.AdmissionResult{State: "rejected", Code: "invalid_command"}
		if eligible && (c.Kind == chat.CommandStop || c.Kind == chat.CommandReplace) {
			result = state.claimControl(c)
			if result.State == "accepted" && turnCancel != nil {
				turnCancel()
			}
			return result
		}
		if eligible && c.Kind == chat.CommandMessage && c.Sequence > state.LastMessageSequence && len(state.Inbox) < in.MaxInbox {
			// No yield: persist a receipt only AFTER this replayable enqueue decision.
			state.Inbox = append(state.Inbox, chat.TurnRef{ConversationID: in.ConversationID, TurnID: c.TurnID})
			state.LastMessageSequence = c.Sequence
			result = chat.AdmissionResult{State: "accepted"}
		}
		return result
	}
	handler := func(u workflow.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
		reject := chat.AdmissionResult{State: "rejected", Code: "invalid_command"}
		operations++ // Count before the first yield, including mutex waiters.
		if err := mutex.Lock(u); err != nil {
			return chat.AdmissionResult{}, err
		}
		defer mutex.Unlock()
		hash, err := (chat.CommandInput{ConversationID: c.ConversationID, RequestID: c.RequestID, Kind: c.Kind, Payload: c.Payload}).Digest()
		if err != nil || hash != c.PayloadDigest || c.ConversationID != in.ConversationID || c.Model != in.Model || c.WorkflowID != "web-chat/"+string(in.ConversationID) || !chat.ValidID(string(c.CommandID)) || (c.TurnID != "" && !chat.ValidID(string(c.TurnID))) || c.Sequence <= 0 {
			return reject, nil
		}
		if info := workflow.GetCurrentUpdateInfo(u); info != nil && info.ID != string(c.CommandID) {
			return reject, nil
		}
		eligible := (c.Kind == chat.CommandMessage || c.Kind == chat.CommandReplace) && len(c.Payload.AttachmentIDs) == 0 && c.Payload.Text != "" && len(c.Payload.Text) <= 65536 && chat.ValidID(string(c.TurnID))
		if c.Kind == chat.CommandStop {
			eligible = c.TurnID == ""
		}
		if len(state.PendingAdmissions) > 0 {
			return handoff(c, "check", eligible, chat.AdmissionResult{})
		}
		var saved chat.AdmissionResult
		if err = workflow.ExecuteActivity(webStorageContext(u), WebCheckCommandActivity, c).Get(u, &saved); err != nil {
			if u.Err() == nil && retryWebStorage(err) {
				return handoff(c, "check", eligible, chat.AdmissionResult{})
			}
			admissionErr = err
			return chat.AdmissionResult{}, err
		}
		if saved.State != "" {
			return saved, nil
		}
		result := admit(c.Identity(), eligible)
		state.DecidingAdmission = &chat.AdmissionCheckpoint{Command: c.Identity(), Phase: "receipt", Eligible: eligible, Result: result}
		defer func() { state.DecidingAdmission = nil }()
		if err = workflow.ExecuteActivity(webStorageContext(u), WebAdmissionActivity, chat.Admission{Command: c, Result: result}).Get(u, nil); err != nil {
			if u.Err() == nil && retryWebStorage(err) {
				return handoff(c, "receipt", eligible, result)
			}
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
		}, workflow.UpdateHandlerOptions{Validator: func(_ workflow.Context, c chat.SessionCommand) error {
			// Rejection before admission is retryable with the same ID. Accepted
			// handlers can return pending only after recording a forced-CAN handoff.
			control := c.Kind == chat.CommandStop || c.Kind == chat.CommandReplace
			// One bounded emergency slot can stop active work after normal
			// admission fills the run budget. It cannot admit an unbounded
			// stream of stale controls while inference delays rollover.
			reservedControl := control && operations <= in.ContinueAfter && state.Control == nil && state.ActiveTurn != "" && c.Payload.ExpectedActiveTurnID == state.ActiveTurn && state.RecoveryPhase == "start"
			if ((operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested()) && !reservedControl) || (state.Status == "recovering" && !control) || state.RecoveringAdmissions > 0 {
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
		sleepCtx := ctx
		if turnCtx != nil && state.Control == nil {
			sleepCtx = turnCtx
		}
		err = workflow.Sleep(sleepCtx, state.RecoveryDelay)
		if ctx.Err() == nil && state.Control != nil {
			return nil
		}
		return err
	}
	for {
		if err := workflow.Await(ctx, func() bool {
			return len(state.PendingAdmissions) > 0 || state.ActiveTurn != "" || len(state.Inbox) > 0 || admissionErr != nil || operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested()
		}); err != nil {
			return err
		}
		if admissionErr != nil {
			return admissionErr
		}
		if operations >= in.ContinueAfter || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			// Every logical future is joined here. Remote invocation cleanup is
			// independently gated by the durable ledger: join/cancel_join carry
			// only IDs and cannot settle or advance until that barrier confirms.
			// Pending handlers must still finish first.
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return err
			}
			if admissionErr != nil {
				return admissionErr
			}
			if state.Control != nil {
				state.Status = "recovering"
			}
			in.Carry = &state
			return workflow.NewContinueAsNewError(ctx, WebSessionName, in)
		}
		if len(state.PendingAdmissions) > 0 {
			cp := &state.PendingAdmissions[0]
			var saved chat.AdmissionResult
			if err := workflow.ExecuteActivity(webStorageContext(ctx), WebResumeAdmissionActivity, *cp).Get(ctx, &saved); err != nil {
				if err = recoverTurn(err); err != nil {
					return err
				}
				continue
			}
			operations++
			if cp.Phase == "check" && saved.State == "" {
				cp.Result = admit(cp.Command, cp.Eligible)
				cp.Phase = "receipt"
			} else {
				state.PendingAdmissions = state.PendingAdmissions[1:]
				state.RecoveringAdmissions = len(state.PendingAdmissions)
			}
			if state.ActiveTurn == "" {
				state.Status = "idle"
			}
			continue
		}
		if state.ActiveTurn == "" {
			state.ActiveTurn = state.Inbox[0].TurnID
			state.Inbox = state.Inbox[1:]
			state.RecoveryPhase = "start"
		}
		ref := chat.TurnRef{ConversationID: in.ConversationID, TurnID: state.ActiveTurn}
		if turnCtx == nil {
			turnCtx, turnCancel = workflow.WithCancel(ctx)
		}
		if state.Control != nil && state.PendingResult == nil {
			reason := "stopped"
			if state.Control.Command.Kind == chat.CommandReplace {
				reason = "replaced"
			}
			state.PendingResult = &chat.TurnResult{TurnRef: ref, Status: "cancelled", Reason: reason}
			state.RecoveryPhase = "cancel_mark"
		}
		if state.RecoveryPhase == "cancel_mark" {
			// Do not forget a stop claim until its permanent receipt exists.
			// Each handler is bounded; exhaustion hands off before completion.
			if err := workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) }); err != nil {
				return err
			}
			if len(state.PendingAdmissions) > 0 {
				continue
			}
			state.Status = "cancelling"
			if err := workflow.ExecuteActivity(webStorageContext(ctx), WebMarkCancellingActivity, ref).Get(ctx, nil); err != nil {
				if err = recoverTurn(err); err != nil {
					return err
				}
				continue
			}
			state.RecoveryPhase = "cancel_join"
		}
		if state.RecoveryPhase == "start" {
			state.Status = "running"
			if err := workflow.ExecuteActivity(webStorageContext(turnCtx), WebStartTurnActivity, ref).Get(join, nil); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if state.Control != nil {
					continue
				}
				if err = recoverTurn(err); err != nil {
					return err
				}
				continue
			}
			if state.Control != nil {
				continue
			}
			state.RecoveryDelay = 0
			mctx := workflow.WithActivityOptions(turnCtx, workflow.ActivityOptions{TaskQueue: WebLLMQueue, StartToCloseTimeout: 10 * time.Minute, ScheduleToCloseTimeout: 25 * time.Minute, HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Second, NonRetryableErrorTypes: []string{"InvalidRequest", "ModelNotFound"}}})
			// Service shutdown is not user cancellation; genuine cancellation joins.
			err := workflow.ExecuteActivity(mctx, WebModelActivity, ref).Get(join, nil)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if state.Control != nil {
				// Neither the final error nor success proves older retries joined.
				// All outcomes pass through the same sealed invocation barrier.
				continue
			}
			result := chat.TurnResult{TurnRef: ref, Status: "completed"}
			if err != nil {
				result.Status = "failed"
				result.Reason = "model_error"
			}
			state.PendingResult = &result
			state.RecoveryPhase = "join"
		}
		if state.RecoveryPhase == "join" || state.RecoveryPhase == "cancel_join" {
			var pending int64
			if err := workflow.ExecuteActivity(webStorageContext(ctx), WebSealModelActivity, ref).Get(ctx, &pending); err != nil {
				if err = recoverTurn(err); err != nil {
					return err
				}
				continue
			}
			if pending != 0 {
				state.UnconfirmedModelInvocations = pending
				state.Status = "cleanup_interrupted"
				if state.Control != nil {
					state.Status = "cancelling_interrupted"
				}
				// Durable missing acknowledgments, not elapsed time. Never reset,
				// settle, CAN into newer work, or assume an older attempt joined.
				return workflow.Await(ctx, func() bool { return false })
			}
			state.RecoveryPhase = "settle"
			if state.Control != nil {
				state.RecoveryPhase = "cancel_settle"
			}
		}
		state.Status = "persisting"
		if state.Control != nil {
			state.Status = "cancelling"
		}
		if err := workflow.ExecuteActivity(webStorageContext(ctx), WebPersistTurnActivity, *state.PendingResult).Get(ctx, nil); err != nil {
			if err = recoverTurn(err); err != nil {
				return err
			}
			continue
		}
		if state.PendingResult.Status == "completed" {
			state.Completed++
		} else if state.PendingResult.Status == "cancelled" {
			state.Cancelled++
		} else {
			state.Failed++
		}
		next := chat.TurnID("")
		if state.Control != nil && state.Control.Command.Kind == chat.CommandReplace {
			next = state.Control.Command.TurnID
		}
		state.Control = nil
		turnCancel()
		turnCtx, turnCancel = nil, nil
		state.ActiveTurn = ""
		state.PendingResult = nil
		state.RecoveryPhase = ""
		state.RecoveryDelay = 0
		state.Status = "idle"
		if next != "" {
			state.ActiveTurn = next
			state.RecoveryPhase = "start"
			state.Status = "recovering" // joined ID-only checkpoint if rollover is due
		}
	}
}
