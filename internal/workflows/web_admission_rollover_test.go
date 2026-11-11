package workflows_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestWebAdmissionHandoffCannotCarryUnboundedIdentity(t *testing.T) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	cmd.TurnID = chat.TurnID(strings.Repeat("x", 10000))
	e.RegisterActivityWithOptions(func(context.Context, chat.SessionCommand) (chat.AdmissionResult, error) {
		return chat.AdmissionResult{}, errors.New("storage unavailable")
	}, activity.RegisterOptions{Name: w.WebCheckCommandActivity, DisableAlreadyRegisteredCheck: true})
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("message", string(cmd.CommandID), &testsuite.TestUpdateCallback{OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, chat.AdmissionResult{State: "rejected", Code: "invalid_command"}, v)
		}, OnReject: func(err error) { t.Error(err) }}, cmd)
	}, 0)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, ContinueAfter: 1})
}

func TestWebAdmissionOutageMustReachBoundedRollover(t *testing.T) {
	for _, stage := range []string{"check", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			e, c := setupWeb(t)
			cmd := webCommand(c, 1)
			var calls atomic.Int32
			fail := func() error { calls.Add(1); return errors.New("persistent storage outage") }
			if stage == "check" {
				e.RegisterActivityWithOptions(func(context.Context, chat.SessionCommand) (chat.AdmissionResult, error) {
					return chat.AdmissionResult{}, fail()
				}, activity.RegisterOptions{Name: w.WebCheckCommandActivity, DisableAlreadyRegisteredCheck: true})
			} else {
				e.RegisterActivityWithOptions(func(context.Context, chat.Admission) error { return fail() }, activity.RegisterOptions{Name: w.WebAdmissionActivity, DisableAlreadyRegisteredCheck: true})
			}
			e.RegisterDelayedCallback(func() {
				e.UpdateWorkflow("message", string(cmd.CommandID), &testsuite.TestUpdateCallback{OnReject: func(err error) { t.Error(err) }, OnComplete: func(v any, err error) {
					require.NoError(t, err)
					require.Equal(t, chat.AdmissionResult{State: "pending"}, v)
				}}, cmd)
			}, 0)
			e.RegisterDelayedCallback(e.CancelWorkflow, 2*time.Minute)
			e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, ContinueAfter: 1})
			var next *workflow.ContinueAsNewError
			require.ErrorAs(t, e.GetWorkflowError(), &next, "accepted handler must finish a resumable handoff, not hold this run forever")
			require.EqualValues(t, 5, calls.Load(), "one bounded batch before rollover")
		})
	}
}
