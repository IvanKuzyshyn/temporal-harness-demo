package workflows

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestContextRefreshVersionBranches(t *testing.T) {
	for _, mode := range []string{"legacy", "fresh", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			for run := 1; run <= 2; run++ {
				version := workflow.Version(1)
				if mode == "legacy" || mode == "upgrade" && run == 1 {
					version = workflow.DefaultVersion
				}
				env.OnGetVersion(fmt.Sprintf("project-context-refresh/%d", run), workflow.DefaultVersion, workflow.Version(1)).Return(version).Once()
			}
			env.RegisterActivityWithOptions(func(context.Context, EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: EmitActivity})
			var mu sync.Mutex
			var inputs []GatherInput
			attempts := 0
			env.RegisterActivityWithOptions(func(_ context.Context, in GatherInput) (AppendResult, error) {
				mu.Lock()
				defer mu.Unlock()
				inputs = append(inputs, in)
				if in.Kind == "evidence" {
					attempts++
					if attempts == 1 {
						return AppendResult{}, temporal.NewApplicationError("retry", "ReadTransient")
					}
				}
				return AppendResult{TranscriptRef: "ref"}, nil
			}, activity.RegisterOptions{Name: GatherActivity})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				r := runtime{cfg: Input{Workdir: "/selected/root"}, state: State{RunCounter: 1}, inputID: "input-1"}
				if err := r.gatherContext(ctx); err != nil {
					return err
				}
				if !r.state.ContextGathered {
					return fmt.Errorf("missing gathered state")
				}
				// Simulate an open upgraded session (including carried state), not a fresh
				// process that accidentally clears ContextGathered.
				r.state.RunCounter = 2
				r.inputID = "input-2"
				return r.gatherContext(ctx)
			})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, in := range inputs {
				if in.Workdir != "/selected/root" {
					t.Fatal(in)
				}
				counts[in.InputID+"/"+in.Kind]++
			}
			want := map[string]int{}
			if mode != "fresh" {
				for _, kind := range []string{"git", "tree", "readme", "todo"} {
					want["/"+kind] = 1
				}
			}
			switch mode {
			case "fresh":
				want["input-1/evidence"] = 2
				want["input-2/evidence"] = 1
			case "upgrade":
				want["input-2/evidence"] = 2
			}
			if !reflect.DeepEqual(counts, want) {
				t.Fatalf("inputs=%+v counts=%v want=%v", inputs, counts, want)
			}
			env.AssertExpectations(t)
		})
	}
}
