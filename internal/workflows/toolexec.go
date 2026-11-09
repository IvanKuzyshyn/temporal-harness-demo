package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/workflow"
)

// executeBatch is intentionally fail-closed until the parallel/policy unit
// supplies preflight and safe execution. Every requested call gets a result.
func (r *runtime) executeBatch(ctx workflow.Context, calls []llm.ToolCall) (BatchResult, error) {
	batch := BatchResult{Results: make([]tools.Result, 0, len(calls))}
	for _, call := range calls {
		if _, err := r.approveTool(ctx, call); err != nil {
			return batch, err
		}
		result := tools.Result{ToolCallID: call.ID, IsError: true, Output: "tool execution is not supported in the basic session"}
		if call.Name == "delegate" {
			result = r.delegate(ctx, call)
		}
		batch.Results = append(batch.Results, result)
	}
	return batch, nil
}
func (r *runtime) gatherContext(workflow.Context) error { return nil }

// modelTools is populated by the tool-policy implementation.
func (r *runtime) modelTools() []llm.ToolDefinition { return nil }
