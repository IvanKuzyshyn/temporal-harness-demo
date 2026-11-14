package workflows_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestStopAdmittedAtRunBudget(t *testing.T) {
	e, c := setupWeb(t)
	target, follow := webCommand(c, 1), webCommand(c, 2)
	stop := controlCommand(target, 3, chat.CommandStop)
	e.RegisterActivityWithOptions(func(ctx context.Context, _ chat.TurnRef) error { <-ctx.Done(); return ctx.Err() }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(target.CommandID), t, target) }, 0)
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(follow.CommandID), t, follow) }, time.Second)
	admitted := false
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("stop", string(stop.CommandID), &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Errorf("stop at run budget rejected: %v", err) }, OnComplete: func(v any, err error) {
			require.NoError(t, err)
			admitted = v.(chat.AdmissionResult).State == "accepted"
		}}, stop)
	}, 2*time.Second)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, 3*time.Second)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, ContinueAfter: 2})
	require.True(t, admitted, "a full normal run budget must not block stopping active inference")
	var state w.WebSessionState
	v, err := e.QueryWorkflow("state")
	require.NoError(t, err)
	require.NoError(t, v.Get(&state))
	require.NotNil(t, state.Control)
	require.Equal(t, follow.TurnID, state.Inbox[0].TurnID)
}

func TestStopInterruptsStartRecoveryBackoffAndPreservesFollowUp(t *testing.T) {
	e, c := setupWeb(t)
	target, follow := webCommand(c, 1), webCommand(c, 2)
	stop := controlCommand(target, 3, chat.CommandStop)
	e.RegisterActivityWithOptions(func(_ context.Context, ref chat.TurnRef) error {
		if ref.TurnID == target.TurnID {
			return errors.New("storage unavailable")
		}
		return nil
	}, activity.RegisterOptions{Name: w.WebStartTurnActivity, DisableAlreadyRegisteredCheck: true})
	var models []chat.TurnID
	var cancelled bool
	e.RegisterActivityWithOptions(func(_ context.Context, ref chat.TurnRef) error { models = append(models, ref.TurnID); return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebMarkCancellingActivity})
	e.RegisterActivityWithOptions(func(_ context.Context, r chat.TurnResult) error {
		if r.TurnID == target.TurnID {
			require.Equal(t, "cancelled", r.Status)
			cancelled = true
		}
		return nil
	}, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflowNoRejection("message", string(target.CommandID), t, target)
		e.UpdateWorkflowNoRejection("message", string(follow.CommandID), t, follow)
	}, 0)
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("stop", string(stop.CommandID), &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, "accepted", v.(chat.AdmissionResult).State)
		}}, stop)
	}, 16*time.Second)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
	require.True(t, cancelled)
	require.Equal(t, []chat.TurnID{follow.TurnID}, models)
}

// Invalid carry must fail before any activity can see attacker-sized metadata.
func TestReplacementCarryRejectsUnboundedControlIdentity(t *testing.T) {
	e, c := setupWeb(t)
	target := webCommand(c, 1)
	replacement := controlCommand(target, 2, chat.CommandReplace)
	identity := replacement.Identity()
	identity.RequestID = strings.Repeat("x", 100000)
	state := w.WebSessionState{Status: "recovering", ActiveTurn: target.TurnID, RecoveryPhase: "cancel_mark", PendingResult: &chat.TurnResult{TurnRef: chat.TurnRef{ConversationID: c, TurnID: target.TurnID}, Status: "cancelled", Reason: "replaced"}, Control: &w.WebControl{Command: identity}}
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { t.Error("invalid carry reached activity"); return nil }, activity.RegisterOptions{Name: w.WebMarkCancellingActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Carry: &state})
	require.ErrorContains(t, e.GetWorkflowError(), "invalid carry")
}
