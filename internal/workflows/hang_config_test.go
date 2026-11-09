package workflows

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestBatchCarriesConfiguredHangTimeout(t *testing.T) {
	var cfg Input
	if err := json.Unmarshal([]byte(`{"hangTimeout":2500000000}`), &cfg); err != nil {
		t.Fatal(err)
	}
	want := 2500 * time.Millisecond
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	preflights, executions := 0, 0
	env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (PreflightResult, error) {
		preflights++
		if in.HangTimeout != want {
			t.Errorf("preflight hang timeout = %v, want %v", in.HangTimeout, want)
		}
		return PreflightResult{Allowed: true, Mode: tools.Parallel, Replay: tools.Safe, ActionHash: "validated"}, nil
	}, activity.RegisterOptions{Name: PreflightActivity})
	env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (tools.Result, error) {
		executions++
		if in.HangTimeout != want {
			t.Errorf("execution hang timeout = %v, want %v", in.HangTimeout, want)
		}
		return tools.Result{ToolCallID: in.Call.ID, Output: "ok"}, nil
	}, activity.RegisterOptions{Name: ToolActivity})
	env.RegisterActivityWithOptions(func(context.Context, EmitInput) (uint64, error) { return 0, nil }, activity.RegisterOptions{Name: EmitActivity})
	env.ExecuteWorkflow(func(ctx workflow.Context) (BatchResult, error) {
		r := runtime{cfg: cfg, inputID: "configured-timeout"}
		return r.executeBatch(ctx, []llm.ToolCall{{ID: "read", Name: "read_file", Args: json.RawMessage(`{"path":"README.md"}`)}})
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if preflights != 1 || executions != 1 {
		t.Fatalf("calls: preflight=%d execution=%d", preflights, executions)
	}
}
