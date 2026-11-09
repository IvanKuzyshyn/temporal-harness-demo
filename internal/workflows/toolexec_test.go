package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestBatchScheduling(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var mu sync.Mutex
			active, peak := 0, 0
			var ends []string
			var executed []string
			env.RegisterActivityWithOptions(func(_ context.Context, in EmitInput) (uint64, error) {
				mu.Lock()
				defer mu.Unlock()
				if in.Type == events.ToolEnd {
					raw, _ := json.Marshal(in.Data)
					var data events.ToolData
					_ = json.Unmarshal(raw, &data)
					ends = append(ends, data.ToolCallID)
				}
				return 1, nil
			}, activity.RegisterOptions{Name: EmitActivity})
			env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (PreflightResult, error) {
				if in.InputID != "logical" {
					t.Error("missing input ID")
				}
				var registry tools.LocalRegistry
				def, _ := registry.Lookup(in.Call.Name)
				return PreflightResult{Allowed: true, RequiresApproval: def.Replay == tools.Unsafe, Mode: def.ExecutionMode, Replay: def.Replay, ActionHash: "hash"}, nil
			}, activity.RegisterOptions{Name: PreflightActivity})
			env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (tools.Result, error) {
				mu.Lock()
				active++
				if active > peak {
					peak = active
				}
				executed = append(executed, in.Call.ID)
				mu.Unlock()
				if in.Call.ID == "0" {
					time.Sleep(80 * time.Millisecond)
				} else {
					time.Sleep(5 * time.Millisecond)
				}
				mu.Lock()
				active--
				mu.Unlock()
				if in.Call.ID == "2" {
					return tools.Result{}, fmt.Errorf("read failed")
				}
				return tools.Result{Output: in.Call.ID}, nil
			}, activity.RegisterOptions{Name: ToolActivity})
			calls := make([]llm.ToolCall, 7)
			for i := range calls {
				calls[i] = llm.ToolCall{ID: fmt.Sprint(i), Name: "read_file", Args: []byte(`{"path":"x"}`)}
			}
			if mixed {
				calls[6].Name = "write_file"
			}
			env.ExecuteWorkflow(func(ctx workflow.Context) (BatchResult, error) {
				r := runtime{inputID: "logical", state: State{ActiveTools: append([]llm.ToolCall(nil), calls...)}}
				result, err := r.executeBatch(ctx, calls)
				if len(r.state.ActiveTools) != 0 {
					return result, fmt.Errorf("active tools remain")
				}
				return result, err
			})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var result BatchResult
			if err := env.GetWorkflowResult(&result); err != nil {
				t.Fatal(err)
			}
			if len(result.Results) != len(calls) {
				t.Fatal(result)
			}
			for i, res := range result.Results {
				if res.ToolCallID != calls[i].ID {
					t.Fatal("order changed")
				}
			}
			if !result.Results[2].IsError || result.Results[5].IsError {
				t.Fatal("failure stopped batch")
			}
			failedAttempts := 0
			for _, id := range executed {
				if id == "2" {
					failedAttempts++
				}
			}
			if failedAttempts != 3 {
				t.Fatalf("safe attempts=%d", failedAttempts)
			}
			if peak > 4 {
				t.Fatalf("peak=%d", peak)
			}
			if mixed {
				if len(ends) != len(calls) || ends[0] != "0" {
					t.Fatalf("sequential completion order: %v", ends)
				}
				if peak != 1 || !result.Results[6].IsError {
					t.Fatalf("unsafe allowed or parallel mixed: %d %+v", peak, result)
				}
				for _, id := range executed {
					if id == "6" {
						t.Fatal("unsafe executed")
					}
				}
			} else {
				if peak < 2 || ends[0] == "0" {
					t.Fatalf("not concurrent/completion ordered: peak=%d ends=%v", peak, ends)
				}
			}
		})
	}
}

func TestUnsafeCommandRecovery(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	attempts, recoveries := 0, 0
	env.RegisterActivityWithOptions(func(ctx context.Context, in ToolInput) (CommandResult, error) {
		attempts++
		if in.InputID != "logical" {
			t.Error("missing logical ID")
		}
		info := activity.GetInfo(ctx)
		if info.HeartbeatTimeout != 10*time.Second {
			t.Errorf("heartbeat=%v", info.HeartbeatTimeout)
		}
		return CommandResult{}, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_HEARTBEAT, nil)
	}, activity.RegisterOptions{Name: CommandActivity})
	env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (CommandResult, error) {
		recoveries++
		if in.InputID != "logical" {
			t.Error("missing recovery logical ID")
		}
		return CommandResult{Tail: "persisted partial output", LogRef: "log"}, nil
	}, activity.RegisterOptions{Name: RecoverCommandActivity})
	env.ExecuteWorkflow(func(ctx workflow.Context) (tools.Result, error) {
		r := runtime{}
		return r.executeAllowed(ctx, ToolInput{InputID: "logical", Call: llm.ToolCall{ID: "command", Name: "run_command"}}, tools.Unsafe), nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var result tools.Result
	env.GetWorkflowResult(&result)
	if attempts != 1 || recoveries != 1 || !result.IsError || !result.Interrupted || result.Output != "persisted partial output" {
		t.Fatalf("attempts=%d recoveries=%d result=%+v", attempts, recoveries, result)
	}
}

func TestGatherContextFutures(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context, EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: EmitActivity})
	var mu sync.Mutex
	active, peak, count := 0, 0, 0
	env.RegisterActivityWithOptions(func(_ context.Context, in GatherInput) (AppendResult, error) {
		mu.Lock()
		active++
		count++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return AppendResult{TranscriptRef: "parent", LastSeq: 2}, nil
	}, activity.RegisterOptions{Name: GatherActivity})
	env.ExecuteWorkflow(func(ctx workflow.Context) (State, error) {
		r := runtime{state: State{TranscriptRef: "parent"}}
		err := r.gatherContext(ctx)
		if err == nil {
			err = r.gatherContext(ctx)
		}
		return r.state, err
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state State
	env.GetWorkflowResult(&state)
	if !state.ContextGathered || peak != 4 || count != 4 {
		t.Fatalf("state=%+v peak=%d count=%d", state, peak, count)
	}
}
