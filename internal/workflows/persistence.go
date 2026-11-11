package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// persistJoinedToolResults requires all tool futures, including command recovery,
// to have joined. Stop must not cancel their durable result tail, even on normal
// runs where Stop arrives during the append. This idempotent append never runs a
// tool and retains activityContext's 3-minute schedule/3-attempt policy.
func (r *runtime) persistJoinedToolResults(ctx workflow.Context, entryID string, messages ...llm.Message) error {
	persistCtx, finishPersistence := workflow.NewDisconnectedContext(ctx)
	defer finishPersistence()
	if err := r.appendMessages(persistCtx, entryID, messages...); err != nil {
		// Fail closed rather than expose an unreconciled tail to Continue.
		r.toolPersistenceErr = temporal.NewNonRetryableApplicationError("finalize tool results", "ToolResultPersistence", err)
		return r.toolPersistenceErr
	}
	return nil
}
