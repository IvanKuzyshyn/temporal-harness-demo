package workflows

import (
	"context"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestSettlementQueryStatusDiffersFromEventStatus(t *testing.T) {
	for _, failed := range []bool{false, true} {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		wantState, wantEvent := "settled", "done"
		if failed {
			wantState, wantEvent = "failed", "failed"
		}
		env.RegisterActivityWithOptions(func(_ context.Context, in EmitInput) (uint64, error) {
			if in.Type == events.AgentSettled {
				data := in.Data.(map[string]interface{})
				if data["status"] != wantEvent {
					t.Errorf("event=%v", data)
				}
			}
			return 1, nil
		}, activity.RegisterOptions{Name: EmitActivity})
		env.SetOnActivityStartedListener(func(_ *activity.Info, _ context.Context, _ converter.EncodedValues) {
			value, err := env.QueryWorkflow(StateQuery)
			if err != nil {
				t.Error(err)
				return
			}
			var state State
			if err := value.Get(&state); err != nil {
				t.Error(err)
				return
			}
			if state.Status != wantState {
				t.Errorf("status=%s want=%s", state.Status, wantState)
			}
		})
		env.ExecuteWorkflow(func(ctx workflow.Context) (State, error) {
			r := &runtime{state: State{Status: "working"}}
			if failed {
				r.state.LastRunFailure = &Failure{Reason: "model_error"}
			}
			if err := workflow.SetQueryHandler(ctx, StateQuery, func() (State, error) { return r.state, nil }); err != nil {
				return State{}, err
			}
			r.settle(ctx)
			return r.state, nil
		})
		if err := env.GetWorkflowError(); err != nil {
			t.Fatal(err)
		}
		var final State
		if err := env.GetWorkflowResult(&final); err != nil {
			t.Fatal(err)
		}
		if final.Status != "idle" {
			t.Fatal(final.Status)
		}
	}
}
