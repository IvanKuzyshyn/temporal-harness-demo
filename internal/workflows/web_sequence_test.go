package workflows_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"testing"
	"time"
)

func TestUnadmittedOldSequenceIsNotFalseAcceptance(t *testing.T) {
	e, c := setupWeb(t)
	cmd := webCommand(c, 1)
	e.RegisterDelayedCallback(func() {
		e.UpdateWorkflow("message", string(cmd.CommandID), &testsuite.TestUpdateCallback{OnComplete: func(v any, err error) {
			require.NoError(t, err)
			require.Equal(t, chat.AdmissionResult{State: "rejected", Code: "invalid_command"}, v)
		}, OnReject: func(err error) { t.Error(err) }}, cmd)
	}, 0)
	e.RegisterDelayedCallback(func() { e.CancelWorkflow() }, time.Minute)
	e.ExecuteWorkflow(w.WebChatSessionWorkflow, w.WebSessionInput{ConversationID: c, Model: "qwen3.5:9b", Carry: &w.WebSessionState{LastMessageSequence: 100}})
}
