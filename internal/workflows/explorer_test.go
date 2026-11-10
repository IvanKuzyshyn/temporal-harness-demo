package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestDelegateCancellationWaitsForCleanup(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	cleaned := false
	env.RegisterActivityWithOptions(func(_ context.Context, in EmitInput) (uint64, error) {
		if in.Type == events.ChildCompleted {
			require.True(t, cleaned)
		}
		return 1, nil
	}, activity.RegisterOptions{Name: EmitActivity})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ ExplorerInput) (ExplorerResult, error) {
		_ = workflow.Sleep(ctx, time.Hour)
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		require.NoError(t, workflow.Sleep(cleanup, time.Second))
		cleaned = true
		return ExplorerResult{}, ctx.Err()
	}, workflow.RegisterOptions{Name: ExplorerName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		r := runtime{scope: events.Scope{WorkflowID: "parent", StreamID: "parent"}}
		childCtx, cancel := workflow.WithCancel(ctx)
		workflow.Go(ctx, func(ctx workflow.Context) { _ = workflow.Sleep(ctx, time.Second); cancel() })
		res := r.delegate(childCtx, llm.ToolCall{ID: "delegate", Args: json.RawMessage(`{"question":"inspect"}`)})
		require.True(t, res.IsError)
		require.True(t, cleaned)
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
}

func TestDelegate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var emitted []EmitInput
			env.RegisterActivityWithOptions(func(_ context.Context, in EmitInput) (uint64, error) { emitted = append(emitted, in); return 1, nil }, activity.RegisterOptions{Name: EmitActivity})
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in ExplorerInput) (ExplorerResult, error) {
				info := workflow.GetInfo(ctx)
				require.Equal(t, "parent/explore-5", info.WorkflowExecution.ID)
				require.Equal(t, SubagentsQueue, info.TaskQueueName)
				require.Equal(t, 15*time.Minute, info.WorkflowExecutionTimeout)
				require.Equal(t, 8, in.MaxSteps)
				require.Equal(t, "question", in.Task)
				require.Equal(t, "../unsafe", in.Path)
				require.Equal(t, "parent-run", in.ParentScope.RunID)
				if fail {
					return ExplorerResult{}, fmt.Errorf("child failed")
				}
				return ExplorerResult{Summary: strings.Repeat("界", 1000), SummaryRef: "child-ref"}, nil
			}, workflow.RegisterOptions{Name: ExplorerName})
			env.ExecuteWorkflow(func(ctx workflow.Context) (tools.Result, error) {
				r := runtime{scope: events.Scope{StreamID: "stream", WorkflowID: "parent", RunID: "parent-run"}, state: State{ChildCounter: 4}}
				res := r.delegate(ctx, llm.ToolCall{ID: "call", Name: "delegate", Args: json.RawMessage(`{"question":"question","path":"../unsafe"}`)})
				require.Equal(t, 5, r.state.ChildCounter)
				require.Len(t, r.state.Children, 1)
				if fail {
					require.Equal(t, "failed", r.state.Children[0].Status)
				} else {
					require.Equal(t, "child-ref", r.state.Children[0].SummaryRef)
				}
				return res, nil
			})
			require.NoError(t, env.GetWorkflowError())
			var result tools.Result
			require.NoError(t, env.GetWorkflowResult(&result))
			require.Equal(t, fail, result.IsError)
			require.Equal(t, "call", result.ToolCallID)
			require.LessOrEqual(t, len(result.Output), 2048)
			require.True(t, utf8.ValidString(result.Output))
			if !fail {
				var summary ExplorerResult
				require.NoError(t, json.Unmarshal(result.Structured, &summary))
				require.Equal(t, "child-ref", summary.SummaryRef)
			}
			require.Len(t, emitted, 2)
			require.Equal(t, events.ChildStarted, emitted[0].Type)
			require.Equal(t, events.ChildCompleted, emitted[1].Type)
			require.Equal(t, "parent/explore-5", result.Details["childWorkflowId"])
			for _, e := range emitted {
				require.Equal(t, "parent", e.Scope.WorkflowID)
				require.Equal(t, "stream", e.Scope.StreamID)
				require.Equal(t, "parent-run", e.Scope.RunID)
				raw, err := json.Marshal(e.Data)
				require.NoError(t, err)
				var data events.ChildData
				require.NoError(t, json.Unmarshal(raw, &data))
				require.Equal(t, "parent/explore-5", data.ChildWorkflowID)
				require.Equal(t, "question", data.Task)
				if e.Type == events.ChildCompleted && !fail {
					require.Equal(t, "child-ref", data.SummaryRef)
				}
				require.NotContains(t, string(raw), `"details"`)
			}
		})
	}
}

func TestExplorerIsolation(t *testing.T) {
	for _, mode := range []string{"success", "model", "tokens", "steps", "default-steps", "default-tokens"} {
		t.Run(mode, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var appended []llm.Message
			var executions []string
			env.RegisterActivityWithOptions(func(_ context.Context, in AppendInput) (AppendResult, error) {
				require.Equal(t, "parent", in.Scope.StreamID)
				require.Equal(t, "parent", in.Scope.ParentWorkflowID)
				require.NotEqual(t, "parent", in.Scope.WorkflowID)
				require.NotEmpty(t, in.Scope.RunID)
				appended = append(appended, in.Messages...)
				return AppendResult{TranscriptRef: "child-transcript"}, nil
			}, activity.RegisterOptions{Name: TranscriptActivity})
			var emitted []EmitInput
			env.RegisterActivityWithOptions(func(_ context.Context, in EmitInput) (uint64, error) {
				emitted = append(emitted, in)
				return 1, nil
			}, activity.RegisterOptions{Name: EmitActivity})
			env.RegisterActivityWithOptions(func(_ context.Context, in ToolInput) (tools.Result, error) {
				executions = append(executions, in.Call.Name)
				if in.Call.ID == "initial-context" {
					var args tools.FileArgs
					require.NoError(t, json.Unmarshal(in.Call.Args, &args))
					require.Equal(t, "../unsafe", args.Path)
				}
				return tools.Result{}, fmt.Errorf("unreadable")
			}, activity.RegisterOptions{Name: ToolActivity})
			turns := 0
			env.RegisterActivityWithOptions(func(ctx context.Context, in LLMInput) (LLMResult, error) {
				info := activity.GetInfo(ctx)
				require.Equal(t, LLMQueue, info.TaskQueue)
				require.Equal(t, 15*time.Second, info.HeartbeatTimeout)
				require.Equal(t, "child-transcript", in.TranscriptRef)
				require.Equal(t, "explorer", in.Scenario)
				require.Equal(t, "/selected/explorer", in.Workdir)
				require.Len(t, in.Tools, 4)
				for _, d := range in.Tools {
					require.Contains(t, []string{"read_file", "list_dir", "grep", "tree"}, d.Name)
				}
				turns++
				if mode == "model" {
					return LLMResult{}, fmt.Errorf("model unavailable")
				}
				if mode == "default-tokens" {
					return LLMResult{Tokens: 16001, Summary: "over"}, nil
				}
				if mode == "tokens" {
					return LLMResult{Tokens: 11, Summary: "over"}, nil
				}
				if turns == 1 || mode == "steps" || mode == "default-steps" {
					return LLMResult{ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file"}, {ID: "d", Name: "delegate"}, {ID: "c", Name: "run_command"}, {ID: "r", Name: "read_file"}}}, nil
				}
				return LLMResult{Summary: strings.Repeat("界", 1000), MessageRef: "actual-message-ref"}, nil
			}, activity.RegisterOptions{Name: LLMActivity})
			steps, tokens := 2, 10
			if strings.HasPrefix(mode, "default-") {
				steps, tokens = 0, 0
			}
			env.ExecuteWorkflow(ExplorerWorkflow, ExplorerInput{ParentScope: events.Scope{StreamID: "parent", WorkflowID: "parent"}, Config: Input{Scenario: "demo", Workdir: "/selected/explorer"}, Task: "inspect", Path: "../unsafe", MaxSteps: steps, TokenBudget: tokens})
			require.Equal(t, events.AgentStart, emitted[0].Type)
			require.Equal(t, events.AgentEnd, emitted[len(emitted)-2].Type)
			require.Equal(t, events.AgentSettled, emitted[len(emitted)-1].Type)
			var settled events.AgentData
			raw, err := json.Marshal(emitted[len(emitted)-1].Data)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &settled))
			wantStatus := "failed"
			if mode == "success" {
				wantStatus = "done"
			}
			require.Equal(t, wantStatus, settled.Status)
			counts := map[events.Type]int{}
			for _, e := range emitted {
				require.Equal(t, "parent", e.Scope.ParentWorkflowID)
				require.Equal(t, "parent", e.Scope.StreamID)
				require.NotEqual(t, "parent", e.Scope.WorkflowID)
				counts[e.Type]++
			}
			require.Equal(t, counts[events.TurnStart], counts[events.TurnEnd])
			require.Equal(t, counts[events.ToolStart], counts[events.ToolEnd])
			require.Positive(t, counts[events.ToolStart])
			if mode != "success" {
				require.Error(t, env.GetWorkflowError())
				if mode == "default-steps" {
					require.Equal(t, 8, turns)
				}
				if mode == "default-tokens" {
					require.Equal(t, 1, turns)
				}
				if mode == "steps" {
					require.Equal(t, 2, turns)
				}
				if mode == "model" {
					require.Equal(t, 2, turns)
				}
				if mode == "tokens" {
					require.Equal(t, 1, turns)
				}
				return
			}
			require.NoError(t, env.GetWorkflowError())
			var result ExplorerResult
			require.NoError(t, env.GetWorkflowResult(&result))
			require.Equal(t, "actual-message-ref", result.SummaryRef)
			require.LessOrEqual(t, len(result.Summary), 2048)
			require.True(t, utf8.ValidString(result.Summary))
			require.Equal(t, []string{"read_file", "read_file", "read_file", "read_file", "read_file", "read_file"}, executions) // safe activity retries only
			require.Equal(t, "system", appended[0].Role)
			require.Len(t, appended, 6)
			require.Equal(t, "system", appended[1].Role)
			require.Contains(t, appended[1].Content, "unreadable")
			for _, m := range appended[2:5] {
				require.Contains(t, m.Content, "not allowed")
			}
			require.Contains(t, appended[5].Content, "unreadable")
		})
	}
}
