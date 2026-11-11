package activities

import (
	"context"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func toolBound(s string) string {
	if len(s) > 8192 {
		s = s[:8192]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
	}
	return s
}

// ExecuteTool is a post-policy primitive. Approval is owned by the workflow.
func (a *Activities) ExecuteTool(ctx context.Context, in workflows.ToolInput) (tools.Result, error) {
	registry, err := tools.New(in.Workdir)
	if err != nil {
		return tools.Result{ToolCallID: in.Call.ID, IsError: true, Output: toolBound(err.Error())}, nil
	}
	defer registry.Close()
	result := registry.Execute(ctx, in.Call)
	result.Output = toolBound(result.Output)
	return result, nil
}

func (a *Activities) PreflightTool(ctx context.Context, in workflows.ToolInput) (workflows.PreflightResult, error) {
	if err := ctx.Err(); err != nil {
		return workflows.PreflightResult{}, err
	}
	var registry tools.LocalRegistry // Lookup is static and does not open a root.
	def, ok := registry.Lookup(in.Call.Name)
	if !ok {
		return workflows.PreflightResult{Reason: "unknown tool"}, nil
	}
	result := workflows.PreflightResult{Mode: def.ExecutionMode, Replay: def.Replay}
	decision, reason := tools.BeforeTool(in.Workdir, in.Call)
	result.Reason = toolBound(reason)
	if decision == "block" {
		return result, nil
	}
	hash, err := tools.ActionHash(in.Call)
	if err != nil {
		result.Reason = toolBound(err.Error())
		return result, nil
	}
	result.Allowed = true
	result.RequiresApproval = decision == "needsApproval"
	result.ActionHash = hash
	result.Preview = toolBound(in.Call.Name + " " + string(in.Call.Args))
	return result, nil
}
