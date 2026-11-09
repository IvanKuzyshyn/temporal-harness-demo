package workflows

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const explorerSummaryBytes = 2048

func explorerAllowed(name string) bool {
	switch name {
	case "read_file", "list_dir", "grep", "tree":
		return true
	}
	return false
}

// ExplorerWorkflow owns a separate transcript. Only activity-owned filesystem
// validation may interpret paths; neither a prompt nor tool metadata is a sandbox.
func ExplorerWorkflow(ctx workflow.Context, in ExplorerInput) (ExplorerResult, error) {
	info := workflow.GetInfo(ctx)
	r := runtime{cfg: in.Config, inputID: "explore", scope: events.Scope{StreamID: in.ParentScope.StreamID, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID, ParentWorkflowID: in.ParentScope.WorkflowID}}
	if err := r.appendMessages(ctx, "explore/system", llm.Message{Role: "system", Content: "Explore read-only and return a concise summary. Never modify files, run commands, or delegate. Task: " + in.Task}); err != nil {
		return ExplorerResult{}, err
	}
	// Initial context follows the same guarded activity route as model reads.
	if in.Path != "" {
		args, _ := json.Marshal(tools.FileArgs{Path: in.Path})
		call := llm.ToolCall{ID: "initial-context", Name: "read_file", Args: args}
		res := r.explorerTool(ctx, call)
		if err := r.appendMessages(ctx, "explore/context", llm.Message{Role: "system", Content: "Initial context: " + res.Output}); err != nil {
			return ExplorerResult{}, err
		}
	}
	defs := []llm.ToolDefinition{}
	for _, d := range (&tools.LocalRegistry{}).Definitions("model") {
		if explorerAllowed(d.Name) {
			defs = append(defs, d.ToolDefinition)
		}
	}
	steps := in.MaxSteps
	if steps <= 0 || steps > 8 {
		steps = 8
	}
	remaining := in.TokenBudget
	if remaining <= 0 {
		remaining = 16000 // Default aggregate reported-token budget for delegated work.
	}
	for turn := 0; turn < steps; turn++ {
		modelCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: LLMQueue, StartToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: 15 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2}})
		var res LLMResult
		if err := workflow.ExecuteActivity(modelCtx, LLMActivity, LLMInput{InputID: r.inputID, Scope: r.scope, Model: r.cfg.Model, DataDir: r.cfg.DataDir, TranscriptRef: r.state.TranscriptRef, Turn: turn, Tools: defs, Scenario: r.cfg.Scenario}).Get(ctx, &res); err != nil {
			return ExplorerResult{}, err
		}
		if res.Tokens > 0 {
			if res.Tokens >= remaining {
				return ExplorerResult{}, fmt.Errorf("explorer token budget exhausted")
			}
			remaining -= res.Tokens
		}
		if len(res.ToolCalls) == 0 {
			return ExplorerResult{Summary: bounded(res.Summary, explorerSummaryBytes), SummaryRef: res.MessageRef}, nil
		}
		messages := make([]llm.Message, 0, len(res.ToolCalls))
		for _, call := range res.ToolCalls {
			result := r.explorerTool(ctx, call)
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, ToolName: call.Name, Content: result.Output})
		}
		if err := r.appendMessages(ctx, fmt.Sprintf("explore/turn/%d/tools", turn), messages...); err != nil {
			return ExplorerResult{}, err
		}
	}
	return ExplorerResult{}, fmt.Errorf("explorer step budget exhausted (%d)", steps)
}

func (r *runtime) explorerTool(ctx workflow.Context, call llm.ToolCall) tools.Result {
	result := tools.Result{ToolCallID: call.ID}
	if !explorerAllowed(call.Name) {
		result.IsError = true
		result.Output = "tool not allowed in read-only explorer: " + call.Name
		return result
	}
	err := workflow.ExecuteActivity(activityContext(ctx), ToolActivity, ToolInput{InputID: r.inputID, Scope: r.scope, DataDir: r.cfg.DataDir, Workdir: r.cfg.Workdir, Call: call}).Get(ctx, &result)
	result.ToolCallID = call.ID
	if err != nil {
		result.IsError = true
		result.Output = bounded(err.Error(), 8192)
	}
	return result
}

func (r *runtime) delegate(ctx workflow.Context, call llm.ToolCall) tools.Result {
	result := tools.Result{ToolCallID: call.ID}
	var args tools.DelegateArgs
	if err := json.Unmarshal(call.Args, &args); err != nil || strings.TrimSpace(args.Question) == "" {
		result.IsError = true
		result.Output = "delegate requires a valid question and path"
		return result
	}
	r.state.ChildCounter++
	id := fmt.Sprintf("%s/explore-%d", r.scope.WorkflowID, r.state.ChildCounter)
	result.Details = map[string]string{"childWorkflowId": id}
	index := len(r.state.Children)
	r.state.Children = append(r.state.Children, ChildState{WorkflowID: id, Status: "working"})
	data := events.ToolData{ToolCallID: call.ID, Name: call.Name, Details: result.Details}
	_ = r.emit(ctx, events.ChildStarted, events.Child, data)
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: id, TaskQueue: SubagentsQueue, ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL, WorkflowExecutionTimeout: 15 * time.Minute})
	var child ExplorerResult
	err := workflow.ExecuteChildWorkflow(childCtx, ExplorerName, ExplorerInput{ParentScope: r.scope, Config: r.cfg, Task: args.Question, Path: args.Path, MaxSteps: 8}).Get(ctx, &child)
	if err != nil {
		result.IsError = true
		result.Output = bounded(err.Error(), explorerSummaryBytes)
		r.state.Children[index].Status = "failed"
	} else {
		child.Summary = bounded(child.Summary, explorerSummaryBytes)
		result.Output = child.Summary
		result.Structured, _ = json.Marshal(child)
		result.Details["summaryRef"] = child.SummaryRef
		r.state.Children[index].Status = "done"
		r.state.Children[index].SummaryRef = child.SummaryRef
	}
	data.IsError = result.IsError
	data.Summary = result.Output
	_ = r.emit(ctx, events.ChildCompleted, events.Child, data)
	return result
}
