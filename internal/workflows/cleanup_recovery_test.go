package workflows_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func TestWebOrdinaryMissingCleanupAckDoesNotBlockFinalizationOrFIFO(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "completed"
		if failed {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			e, c := setupWeb(t)
			target, follow := webCommand(c, 1), webCommand(c, 2)
			e.RegisterActivityWithOptions(func(_ context.Context, ref chat.TurnRef) error {
				if failed && ref.TurnID == target.TurnID {
					return temporal.NewNonRetryableApplicationError("model failed", "InvalidRequest", nil)
				}
				return nil
			}, activity.RegisterOptions{Name: w.WebModelActivity})
			e.RegisterActivityWithOptions(func(_ context.Context, ref chat.TurnRef) (int64, error) {
				if ref.TurnID == target.TurnID {
					return 1, nil
				}
				return 0, nil
			}, activity.RegisterOptions{Name: w.WebSealModelActivity, DisableAlreadyRegisteredCheck: true})
			var results []chat.TurnResult
			e.RegisterActivityWithOptions(func(_ context.Context, result chat.TurnResult) error { results = append(results, result); return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
			e.RegisterDelayedCallback(func() {
				e.UpdateWorkflowNoRejection("message", string(target.CommandID), t, target)
				e.UpdateWorkflowNoRejection("message", string(follow.CommandID), t, follow)
			}, 0)
			e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
			e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
			require.Len(t, results, 2)
			require.Equal(t, target.TurnID, results[0].TurnID)
			require.Equal(t, name, results[0].Status)
			require.Equal(t, follow.TurnID, results[1].TurnID)
			require.Equal(t, "completed", results[1].Status)
		})
	}
}

func TestWebCleanupBarrierSurvivesStorageExhaustionAndContinueAsNew(t *testing.T) {
	e, c := setupWeb(t)
	target := webCommand(c, 1)
	replacement := controlCommand(target, 2, chat.CommandReplace)
	ref := chat.TurnRef{ConversationID: c, TurnID: target.TurnID}
	carry := w.WebSessionState{Status: "recovering", ActiveTurn: target.TurnID, RecoveryPhase: "cancel_join", PendingResult: &chat.TurnResult{TurnRef: ref, Status: "cancelled", Reason: "replaced"}, Control: &w.WebControl{Command: replacement.Identity()}}
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) (int64, error) { return 0, errors.New("storage unavailable") }, activity.RegisterOptions{Name: w.WebSealModelActivity, DisableAlreadyRegisteredCheck: true})
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Carry: &carry, ContinueAfter: 1})
	var rollover *workflow.ContinueAsNewError
	require.ErrorAs(t, e.GetWorkflowError(), &rollover)
	var next w.WebSessionInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(rollover.Input, &next))
	require.Equal(t, "cancel_join", next.Carry.RecoveryPhase)
	require.Equal(t, target.TurnID, next.Carry.ActiveTurn)
	require.Equal(t, replacement.Identity(), next.Carry.Control.Command)
	// Recovered storage cannot convert an outstanding old attempt into a join.
	resumed, _ := setupWeb(t)
	resumed.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) (int64, error) { return 1, nil }, activity.RegisterOptions{Name: w.WebSealModelActivity, DisableAlreadyRegisteredCheck: true})
	resumed.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { t.Error("settlement bypassed cleanup"); return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	resumed.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { t.Error("replacement bypassed cleanup"); return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	resumed.RegisterDelayedCallback(func() { resumed.CancelWorkflow() }, time.Minute)
	resumed.ExecuteWorkflow(w.WebChatSessionWorkflow, next)
	value, err := resumed.QueryWorkflow("state")
	require.NoError(t, err)
	var state w.WebSessionState
	require.NoError(t, value.Get(&state))
	require.Equal(t, "cancelling_interrupted", state.Status)
	require.EqualValues(t, 1, state.UnconfirmedModelInvocations)
	require.Zero(t, state.Cancelled)
}
