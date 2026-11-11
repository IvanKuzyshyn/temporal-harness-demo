package workflows

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestApprovalDefaultDeadlineAndCarriedCounter(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	call := llm.ToolCall{Name: "write_file", Args: []byte(`{"path":"x","content":"new"}`)}
	hash, _ := tools.ActionHash(call)
	env.RegisterActivityWithOptions(func(context.Context, ToolInput) (PreflightResult, error) {
		return PreflightResult{Allowed: true, ActionHash: hash}, nil
	}, activity.RegisterOptions{Name: PreflightActivity})
	env.RegisterActivityWithOptions(func(context.Context, EmitInput) error { return nil }, activity.RegisterOptions{Name: NotifyActivity})
	env.RegisterActivityWithOptions(func(context.Context, EmitInput) (uint64, error) { return 0, nil }, activity.RegisterOptions{Name: EmitActivity})
	var firstRequest string
	rejected := false
	check := func(counter int) {
		v, err := env.QueryWorkflow(StateQuery)
		if err != nil {
			t.Fatal(err)
		}
		var state State
		if err := v.Get(&state); err != nil {
			t.Fatal(err)
		}
		p := state.PendingApproval
		if p == nil || state.ApprovalCounter != counter {
			t.Fatalf("state=%+v", state)
		}
		if counter == 8 {
			firstRequest = p.RequestID
		} else {
			if firstRequest == p.RequestID {
				t.Fatal("reused approval request")
			}
			env.UpdateWorkflow(DecideUpdate, "old-request", &testsuite.TestUpdateCallback{OnReject: func(error) { rejected = true }, OnComplete: func(any, error) { t.Error("old request completed") }}, DecisionInput{RequestID: firstRequest, ActionHash: hash, User: "alice", Approved: true})
		}
	}
	env.RegisterDelayedCallback(func() { check(8) }, time.Second)
	env.RegisterDelayedCallback(func() { check(9) }, 5*time.Minute+time.Second)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		r := runtime{state: State{Status: "working", ApprovalCounter: 7}}
		if err := r.registerApprovalHandlers(ctx); err != nil {
			return err
		}
		_ = workflow.SetQueryHandler(ctx, StateQuery, func() (State, error) { return r.state, nil })
		for i := 0; i < 2; i++ {
			started := workflow.Now(ctx)
			ok, err := r.approveTool(ctx, call)
			if ok || err == nil || err.Error() != "approval timed out" {
				return fmt.Errorf("unexpected default outcome: %v %v", ok, err)
			}
			if elapsed := workflow.Now(ctx).Sub(started); elapsed != 5*time.Minute {
				return fmt.Errorf("default timeout=%v", elapsed)
			}
			if r.state.PendingApproval != nil || r.state.Status != "working" {
				return fmt.Errorf("state leaked")
			}
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if !rejected {
		t.Fatal("old request not rejected")
	}
}
