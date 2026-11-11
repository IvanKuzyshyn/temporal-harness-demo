package workflows_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestDisplayFailuresDoNotSuppressRun(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	registerSessionContext(env, "ref")
	appended := 0
	env.RegisterActivityWithOptions(func(context.Context, w.EmitInput) (uint64, error) {
		return 999, temporal.NewNonRetryableApplicationError("broken display", "display", nil)
	}, activity.RegisterOptions{Name: w.EmitActivity})
	env.RegisterActivityWithOptions(func(context.Context, w.AppendInput) (w.AppendResult, error) {
		appended++
		return w.AppendResult{TranscriptRef: "ref", LastSeq: 2}, nil
	}, activity.RegisterOptions{Name: w.TranscriptActivity})
	env.RegisterActivityWithOptions(func(context.Context, w.LLMInput) (w.LLMResult, error) {
		return w.LLMResult{Summary: "answer", LastSeq: 3}, nil
	}, activity.RegisterOptions{Name: w.LLMActivity})
	env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "one", t, w.MessageInput{Text: "hello"}) }, 0)
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		if appended != 1 || s.Answer != "answer" || s.Status != "idle" || s.LastRunFailure != nil || s.LastSeq != 3 {
			t.Errorf("appended=%d state=%+v", appended, s)
		}
		env.CancelWorkflow()
	}, time.Second)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{})
}

func TestInboxSerializedBudgetWhileModelBusy(t *testing.T) {
	for _, text := range []string{strings.Repeat("a", 64<<10), strings.Repeat("\x00", 64<<10)} {
		t.Run(fmt.Sprintf("byte-%d", text[0]), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			registerSessionContext(env, "ref")
			env.RegisterActivityWithOptions(func(context.Context, w.EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: w.EmitActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.AppendInput) (w.AppendResult, error) {
				return w.AppendResult{TranscriptRef: "ref"}, nil
			}, activity.RegisterOptions{Name: w.TranscriptActivity})
			env.RegisterActivityWithOptions(func(context.Context, w.LLMInput) (w.LLMResult, error) { return w.LLMResult{}, nil }, activity.RegisterOptions{Name: w.LLMActivity})
			env.OnActivity(w.LLMActivity, mock.Anything, mock.Anything).After(time.Hour).Return(w.LLMResult{Summary: "answer"}, nil)
			accepted, rejected := 0, 0
			env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "initial", t, w.MessageInput{Text: "hello"}) }, 0)
			env.RegisterDelayedCallback(func() {
				for i := 0; i < 64; i++ {
					env.UpdateWorkflow(w.MessageUpdate, fmt.Sprint(i), &testsuite.TestUpdateCallback{OnReject: func(error) { rejected++ }, OnComplete: func(_ any, err error) {
						if err != nil {
							t.Error(err)
						}
						accepted++
					}}, w.MessageInput{Text: text, RequestID: fmt.Sprint(i)})
				}
			}, time.Second)
			env.RegisterDelayedCallback(func() {
				s := query(t, env)
				b, err := json.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				if accepted == 0 || rejected == 0 || len(s.Inbox) != accepted || len(b) > (1<<20)+32768 || s.Status != "working" {
					t.Errorf("accepted=%d rejected=%d inbox=%d bytes=%d status=%s", accepted, rejected, len(s.Inbox), len(b), s.Status)
				}
				env.CancelWorkflow()
			}, 2*time.Second)
			env.ExecuteWorkflow(w.SessionWorkflow, w.Input{})
		})
	}
}
