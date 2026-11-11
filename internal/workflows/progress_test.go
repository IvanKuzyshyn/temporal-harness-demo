package workflows_test

import (
	"context"
	"testing"
	"time"

	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestObservedPhasesModesAndFinishedClock(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	registerSessionContext(env, "ref")
	env.RegisterActivityWithOptions(func(context.Context, w.EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: w.EmitActivity})
	env.RegisterActivityWithOptions(func(context.Context, w.AppendInput) (w.AppendResult, error) {
		return w.AppendResult{TranscriptRef: "ref"}, nil
	}, activity.RegisterOptions{Name: w.TranscriptActivity})
	env.RegisterActivityWithOptions(func(context.Context, w.LLMInput) (w.LLMResult, error) { return w.LLMResult{}, nil }, activity.RegisterOptions{Name: w.LLMActivity})
	env.OnActivity(w.GatherActivity, mock.Anything, mock.Anything).Return(w.AppendResult{TranscriptRef: "ref"}, nil).After(3 * time.Second)
	env.OnActivity(w.LLMActivity, mock.Anything, mock.Anything).Return(w.LLMResult{Summary: "answer"}, nil).After(10 * time.Second)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflowNoRejection(w.MessageUpdate, "first", t, w.MessageInput{Text: "explain this project"})
	}, 0)
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		if s.Phase != "context" || s.Mode != "normal" || s.RunStartedAt.IsZero() || s.PhaseStartedAt.IsZero() {
			t.Errorf("context: %+v", s)
		}
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		if s.Phase != "generating" || !s.RunFinishedAt.IsZero() {
			t.Errorf("model: %+v", s)
		}
	}, 5*time.Second)
	var finished time.Time
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		finished = s.RunFinishedAt
		if s.Phase != "settled" || finished.IsZero() || finished.Before(s.RunStartedAt) {
			t.Errorf("settled: %+v", s)
		}
	}, 20*time.Second)
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		if s.RunFinishedAt != finished {
			t.Error("finished time advanced")
		}
		env.UpdateWorkflowNoRejection(w.MessageUpdate, "demo", t, w.MessageInput{Text: "/demo"})
	}, 21*time.Second)
	env.RegisterDelayedCallback(func() {
		s := query(t, env)
		if s.Mode != "demo" || s.Phase != "generating" || !s.RunFinishedAt.IsZero() || !s.RunStartedAt.After(finished) {
			t.Errorf("new run: %+v", s)
		}
		env.CancelWorkflow()
	}, 26*time.Second)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{})
}
