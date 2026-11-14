package workflows_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func webCommand(c chat.ConversationID, n int) chat.SessionCommand {
	in := chat.CommandInput{ConversationID: c, RequestID: uuid.NewString(), Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: fmt.Sprint(n)}}
	h, _ := in.Digest()
	return chat.SessionCommand{CommandID: chat.CommandID(uuid.NewString()), ConversationID: c, TurnID: chat.TurnID(uuid.NewString()), RequestID: in.RequestID, Kind: in.Kind, Payload: in.Payload, PayloadDigest: h, WorkflowID: "web-chat/" + string(c), Model: "qwen3.5:9b", Sequence: int64(n)}
}
func setupWeb(t *testing.T) (*testsuite.TestWorkflowEnvironment, chat.ConversationID) {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	e := s.NewTestWorkflowEnvironment()
	c := chat.ConversationID(uuid.NewString())
	e.RegisterActivityWithOptions(func(context.Context, chat.SessionCommand) (chat.AdmissionResult, error) {
		return chat.AdmissionResult{}, nil
	}, activity.RegisterOptions{Name: w.WebCheckCommandActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.Admission) error { return nil }, activity.RegisterOptions{Name: w.WebAdmissionActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebStartTurnActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) (int64, error) { return 0, nil }, activity.RegisterOptions{Name: w.WebSealModelActivity})
	return e, c
}
func TestQueuedFollowUpsFIFO(t *testing.T) {
	e, c := setupWeb(t)
	cmds := []chat.SessionCommand{webCommand(c, 1), webCommand(c, 2), webCommand(c, 3)}
	var order []chat.TurnID
	e.RegisterActivityWithOptions(func(_ context.Context, r chat.TurnRef) error { order = append(order, r.TurnID); return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() {
		for _, cmd := range cmds {
			e.UpdateWorkflowNoRejection("message", string(cmd.CommandID), t, cmd)
		}
	}, 0)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b"})
	require.Equal(t, []chat.TurnID{cmds[0].TurnID, cmds[1].TurnID, cmds[2].TurnID}, order)
}
func TestWebChatCompletedOnlyAfterPersistence(t *testing.T) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error {
		return temporal.NewNonRetryableApplicationError("DB unavailable", "PersistenceUnavailable", nil)
	}, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(cmd.CommandID), t, cmd) }, 0)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b"})
	require.Error(t, e.GetWorkflowError())
	v, err := e.QueryWorkflow("state")
	require.NoError(t, err)
	var state w.WebSessionState
	require.NoError(t, v.Get(&state))
	require.Zero(t, state.Completed)
	require.NotEqual(t, "completed", state.Status)
}
func TestContinueAsNewCommandDedup(t *testing.T) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	e.RegisterActivityWithOptions(func(context.Context, chat.SessionCommand) (chat.AdmissionResult, error) {
		return chat.AdmissionResult{State: "accepted"}, nil
	}, activity.RegisterOptions{Name: w.WebCheckCommandActivity, DisableAlreadyRegisteredCheck: true})
	calls := 0
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { calls++; return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(cmd.CommandID), t, cmd) }, 0)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	// Durable execution receipt covers the old command after 100 turns; the
	// watermark alone must never falsely accept an unseen earlier command.
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b", Carry: &w.WebSessionState{LastMessageSequence: 101, Completed: 100}})
	require.Zero(t, calls)
}
func TestWebUnsupportedControlsRejected(t *testing.T) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	cmd.Kind = chat.CommandRevoke
	cmd.TurnID = ""
	cmd.Payload = chat.CommandPayload{}
	cmd.PayloadDigest, _ = (chat.CommandInput{ConversationID: c, RequestID: cmd.RequestID, Kind: cmd.Kind}).Digest()
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("revoke", string(cmd.CommandID), &testsuite.TestUpdateCallback{OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, chat.AdmissionResult{State: "rejected", Code: "invalid_command"}, v)
		}, OnReject: func(err error) { t.Error(err) }}, cmd)
	}, 0)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b"})
}
