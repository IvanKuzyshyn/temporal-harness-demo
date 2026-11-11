package activities

import (
	"context"
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func (a *Activities) RunCommand(context.Context, workflows.ToolInput) (workflows.CommandResult, error) {
	return workflows.CommandResult{}, fmt.Errorf("command execution is not supported in the basic session")
}

func (a *Activities) RecoverCommand(context.Context, workflows.ToolInput) (workflows.CommandResult, error) {
	return workflows.CommandResult{}, fmt.Errorf("command recovery is not supported in the basic session")
}
