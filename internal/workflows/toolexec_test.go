package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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
	for _, lastTool := range []string{"read_file", "write_file", "run_command"} {
		t.Run(lastTool, func(t *testing.T) {
			mixed := lastTool != "read_file"
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
			commandDispatches := 0
			env.RegisterActivityWithOptions(func(context.Context, ToolInput) (CommandResult, error) {
				mu.Lock()
				defer mu.Unlock()
				commandDispatches++
				return CommandResult{}, nil
			}, activity.RegisterOptions{Name: CommandActivity})
			calls := make([]llm.ToolCall, 7)
			for i := range calls {
				calls[i] = llm.ToolCall{ID: fmt.Sprint(i), Name: "read_file", Args: []byte(`{"path":"x"}`)}
			}
			calls[6].Name = lastTool
			// Deep snapshot: the parent's model calls must survive active-state removal.
			originalJSON, err := json.Marshal(calls)
			if err != nil {
				t.Fatal(err)
			}
			var original []llm.ToolCall
			if err := json.Unmarshal(originalJSON, &original); err != nil {
				t.Fatal(err)
			}
			env.ExecuteWorkflow(func(ctx workflow.Context) (BatchResult, error) {
				// Match loop.go: active state and the execution batch share storage.
				r := runtime{inputID: "logical", state: State{ActiveTools: calls}}
				result, err := r.executeBatch(ctx, calls)
				if len(r.state.ActiveTools) != 0 {
					return result, fmt.Errorf("active tools remain")
				}
				return result, err
			})
			if !reflect.DeepEqual(calls, original) {
				t.Errorf("original calls mutated: got %+v, want %+v", calls, original)
			}
			if commandDispatches != 0 {
				t.Errorf("denied command dispatched %d times", commandDispatches)
			}
			if mixed {
				for _, id := range executed {
					if id == "6" {
						t.Error("denied unsafe call dispatched")
					}
				}
			}
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
			endCounts := make(map[string]int)
			for _, id := range ends {
				endCounts[id]++
			}
			if len(ends) != len(original) {
				t.Fatalf("completion count: %v", ends)
			}
			for i, res := range result.Results {
				if res.ToolCallID != original[i].ID || endCounts[original[i].ID] != 1 {
					t.Fatalf("original ID/order/exactly-once mismatch: results=%+v ends=%v", result.Results, ends)
				}
				if i != 2 && !(mixed && i == 6) && res.Output != original[i].ID {
					t.Fatalf("call/result binding lost: %+v", res)
				}
			}
			if !result.Results[2].IsError || result.Results[5].IsError {
				t.Fatal("failure stopped batch")
			}
			executionCounts := make(map[string]int)
			for _, id := range executed {
				executionCounts[id]++
			}
			for i, call := range original {
				wantAttempts := 1
				if i == 2 {
					wantAttempts = 3 // Safe activity retries, still one tool result/end.
				} else if mixed && i == 6 {
					wantAttempts = 0
				}
				if executionCounts[call.ID] != wantAttempts {
					t.Errorf("call %s attempts=%d, want %d", call.ID, executionCounts[call.ID], wantAttempts)
				}
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

func TestCommandApplicationErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details []interface{}
		want    CommandResult
	}{
		{
			name: "hung",
			details: []interface{}{CommandResult{
				ExitCode: -1, Tail: "last output before hang", LogRef: "artifacts/command.log", Truncated: true,
			}},
			want: CommandResult{ExitCode: -1, Tail: "last output before hang", LogRef: "artifacts/command.log", Truncated: true},
		},
		{
			name:    "interrupted",
			details: []interface{}{CommandResult{ExitCode: -1, LogRef: "artifacts/command.log", Interrupted: true}},
			want:    CommandResult{ExitCode: -1, LogRef: "artifacts/command.log", Interrupted: true},
		},
		{name: "missing"},
		{name: "malformed", details: []interface{}{"not a CommandResult"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			attempts, recoveries := 0, 0
			env.RegisterActivityWithOptions(func(context.Context, ToolInput) (CommandResult, error) {
				attempts++
				// Temporal discards the normal result on error; only details survive.
				return CommandResult{Tail: "not delivered"}, temporal.NewNonRetryableApplicationError("command hung: no output before timeout", "command", fmt.Errorf("command hung: no output before timeout"), tc.details...)
			}, activity.RegisterOptions{Name: CommandActivity})
			env.RegisterActivityWithOptions(func(context.Context, ToolInput) (CommandResult, error) {
				recoveries++
				return CommandResult{}, nil
			}, activity.RegisterOptions{Name: RecoverCommandActivity})
			env.ExecuteWorkflow(func(ctx workflow.Context) (tools.Result, error) {
				r := runtime{}
				return r.executeAllowed(ctx, ToolInput{InputID: "logical", Call: llm.ToolCall{ID: "command", Name: "run_command"}}, tools.Unsafe), nil
			})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var result tools.Result
			if err := env.GetWorkflowResult(&result); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || recoveries != 0 || !result.IsError || result.ToolCallID != "command" {
				t.Fatalf("attempts=%d recoveries=%d result=%+v", attempts, recoveries, result)
			}
			if tc.want.Tail != "" {
				if result.Output != tc.want.Tail {
					t.Errorf("lost command tail: %+v", result)
				}
			} else if !strings.Contains(result.Output, "command hung") {
				t.Errorf("lost original error: %+v", result)
			}
			wantDetails := map[string]string{"exitCode": fmt.Sprint(tc.want.ExitCode), "logRef": tc.want.LogRef, "truncated": fmt.Sprint(tc.want.Truncated), "executionError": "true"}
			if result.Interrupted != tc.want.Interrupted || !reflect.DeepEqual(result.Details, wantDetails) {
				t.Errorf("lost command metadata: %+v, want %+v", result, tc.want)
			}
		})
	}
}

func TestGatherContextFutures(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	// Preserve the legacy four-way schedule and one-time gathering branch.
	env.OnGetVersion("project-context-refresh/0", workflow.DefaultVersion, workflow.Version(1)).Return(workflow.DefaultVersion)
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
