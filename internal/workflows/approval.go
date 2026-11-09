package workflows

import (
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/workflow"
)

// approveTool must stay pure/deterministic; filesystem validation belongs in an
// activity. In particular tools.BeforeTool must not be called from workflow code.
func (r *runtime) approveTool(workflow.Context, llm.ToolCall) (bool, error) { return false, nil }
func (r *runtime) registerApprovalHandlers(ctx workflow.Context) error {
	return workflow.SetUpdateHandler(ctx, DecideUpdate, r.decide)
}

func (r *runtime) decide(workflow.Context, DecisionInput) error {
	return fmt.Errorf("decide is not supported in the basic session")
}
