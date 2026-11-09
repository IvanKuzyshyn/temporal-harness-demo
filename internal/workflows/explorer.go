package workflows

import (
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/workflow"
)

func ExplorerWorkflow(workflow.Context, ExplorerInput) (ExplorerResult, error) {
	return ExplorerResult{}, fmt.Errorf("exploration is not supported in the basic session")
}
func (r *runtime) delegate(_ workflow.Context, call llm.ToolCall) tools.Result {
	return tools.Result{ToolCallID: call.ID, IsError: true, Output: "delegation is not supported in the basic session"}
}
