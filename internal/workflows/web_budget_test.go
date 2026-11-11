package workflows_test

import (
	"context"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestWebAdmissionBudgetBackpressuresUntilRollover(t *testing.T) {
	e, c := setupWeb(t)
	cmds := []chat.SessionCommand{webCommand(c, 1), webCommand(c, 2), webCommand(c, 3)}
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnRef) error { return nil }, activity.RegisterOptions{Name: w.WebModelActivity})
	e.RegisterActivityWithOptions(func(context.Context, chat.TurnResult) error { return nil }, activity.RegisterOptions{Name: w.WebPersistTurnActivity})
	e.OnActivity(w.WebModelActivity, mock.Anything, mock.Anything).Return(nil).After(time.Minute)
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(cmds[0].CommandID), t, cmds[0]) }, 0)
	e.RegisterDelayedCallback(func() { e.UpdateWorkflowNoRejection("message", string(cmds[1].CommandID), t, cmds[1]) }, time.Second)
	rejected := false
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("message", string(cmds[2].CommandID), &testsuite.TestUpdateCallback{OnReject: func(error) { rejected = true }, OnComplete: func(any, error) { t.Error("run history budget accepted another command while inference was active") }}, cmds[2])
	}, 2*time.Second)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, 3*time.Second)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b", ContinueAfter: 2})
	require.True(t, rejected)
}
