package workflows_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestWebPersistenceExhaustionRetainsAdmittedInbox(t *testing.T) {
	e, c := setupWeb(t)
	first, second, third := webCommand(c, 1), webCommand(c, 2), webCommand(c, 3)
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	var calls atomic.Int32
	e.RegisterActivityWithOptions(func(_ context.Context, r chat.TurnResult) error {
		if r.TurnID == first.TurnID && calls.Add(1) <= 5 {
			return errors.New("storage unavailable")
		}
		return nil
	}, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflowNoRejection("message", string(first.CommandID), t, first)
		e.UpdateWorkflowNoRejection("message", string(second.CommandID), t, second)
	}, 0)
	e.RegisterDelayedCallback(func() {
		v, err := e.QueryWorkflow("state")
		require.NoError(t, err)
		var s w.WebSessionState
		require.NoError(t, v.Get(&s))
		require.Equal(t, "recovering", s.Status)
		require.Zero(t, s.Completed)
		require.Len(t, s.Inbox, 1)
	}, 16*time.Second)
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(third.CommandID), t, third) }, 30*time.Second)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b"})
	v, err := e.QueryWorkflow("state")
	require.NoError(t, err)
	var state w.WebSessionState
	require.NoError(t, v.Get(&state))
	require.EqualValues(t, 3, state.Completed)
	require.Empty(t, state.Inbox)
	require.EqualValues(t, 6, calls.Load())
}

func TestWebRecoveryCancellationDoesNotRetryForever(t *testing.T) {
	for _, stage := range []string{"start", "settle"} {
		t.Run(stage, func(t *testing.T) {
			e, c := setupWeb(t)
			cmd := webCommand(c, 1)
			var calls atomic.Int32
			fail := func() error { calls.Add(1); return errors.New("storage unavailable") }
			e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
			e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error {
				if stage == "settle" {
					return fail()
				}
				return nil
			}, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
			if stage == "start" {
				e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return fail() }, activity.RegisterOptions{Name: w.WebStartTurnActivity, DisableAlreadyRegisteredCheck: true})
			}
			e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(cmd.CommandID), t, cmd) }, 0)
			e.RegisterDelayedCallback(func() {
				v, err := e.QueryWorkflow("state")
				require.NoError(t, err)
				var s w.WebSessionState
				require.NoError(t, v.Get(&s))
				require.Equal(t, "recovering", s.Status)
				e.CancelWorkflow()
			}, 16*time.Second)
			e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
			require.True(t, temporal.IsCanceledError(e.GetWorkflowError()))
			require.EqualValues(t, 5, calls.Load())
		})
	}
}
func TestWebAdmissionStorageExhaustionRetainsUpdate(t *testing.T) {
	for _, stage := range []string{"check", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			e, c := setupWeb(t)
			cmd := webCommand(c, 1)
			var calls atomic.Int32
			failBatch := func() error {
				if calls.Add(1) <= 5 {
					return errors.New("storage unavailable")
				}
				return nil
			}
			if stage == "check" {
				e.RegisterActivityWithOptions(func(context.Context, chat.SessionCommand) (chat.AdmissionResult, error) {
					return chat.AdmissionResult{}, failBatch()
				}, activity.RegisterOptions{Name: w.WebCheckCommandActivity, DisableAlreadyRegisteredCheck: true})
			} else {
				e.RegisterActivityWithOptions(func(context.Context, chat.Admission) error { return failBatch() }, activity.RegisterOptions{Name: w.WebAdmissionActivity, DisableAlreadyRegisteredCheck: true})
			}
			e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
			e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
			accepted := false
			e.RegisterDelayedCallback(func() {
				e.UpdateWorkflow("message", string(cmd.CommandID), &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
					require.NoError(t, err)
					require.Equal(t, chat.AdmissionResult{State: "accepted"}, v)
					accepted = true
				}}, cmd)
			}, 0)
			e.RegisterDelayedCallback(func() {
				require.False(t, accepted)
				v, err := e.QueryWorkflow("state")
				require.NoError(t, err)
				var s w.WebSessionState
				require.NoError(t, v.Get(&s))
				require.Equal(t, 1, s.RecoveringAdmissions)
			}, 16*time.Second)
			e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
			e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c})
			require.True(t, accepted)
			require.EqualValues(t, 6, calls.Load())
			v, err := e.QueryWorkflow("state")
			require.NoError(t, err)
			var s w.WebSessionState
			require.NoError(t, v.Get(&s))
			require.EqualValues(t, 1, s.Completed)
			require.Zero(t, s.RecoveringAdmissions)
		})
	}
}
