package activities

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// ExecuteTool fails closed until the tool-policy implementation lands.
func (a *Activities) ExecuteTool(ctx context.Context, in workflows.ToolInput) (tools.Result, error) {
	return tools.Result{ToolCallID: in.Call.ID, IsError: true, Output: "tool execution is not supported in the basic session"}, nil
}

func (a *Activities) PreflightTool(context.Context, workflows.ToolInput) (workflows.PreflightResult, error) {
	return workflows.PreflightResult{Allowed: false, Reason: "tool execution is not supported in the basic session"}, nil
}
