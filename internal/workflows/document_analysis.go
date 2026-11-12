package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"time"
)

const DocumentAnalysisName = "DocumentAnalysisWorkflow"

func DocumentAnalysisWorkflow(ctx workflow.Context, in chat.DocumentAnalysisInput) (chat.DocumentAnalysisResult, error) {
	var result chat.DocumentAnalysisResult
	if !in.Valid() || workflow.GetInfo(ctx).WorkflowExecution.ID != in.ChildID() {
		return result, temporal.NewNonRetryableApplicationError("invalid document scope", "InvalidRequest", nil)
	}
	opts := workflow.ActivityOptions{TaskQueue: WebDocumentsQueue, StartToCloseTimeout: 3 * time.Minute, ScheduleToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: 15 * time.Second, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Second}}
	work, cancel := workflow.WithCancel(workflow.WithActivityOptions(ctx, opts))
	defer cancel()
	join, _ := workflow.NewDisconnectedContext(ctx)
	if err := workflow.ExecuteActivity(work, "PrepareDocumentAnalysis", in).Get(join, &result); err != nil {
		return result, err
	}
	if result.Chunks < 0 || result.Chunks > in.Budget.MaxChunks || result.ChildID != in.ChildID() {
		return result, temporal.NewNonRetryableApplicationError("invalid document manifest", "InvalidRequest", nil)
	}
	if result.ImageProfile != "" {
		if result.ImageProfile != chat.NarrowImageProfile || result.Chunks != 0 {
			return result, temporal.NewNonRetryableApplicationError("invalid image profile", "InvalidRequest", nil)
		}
		err := workflow.ExecuteActivity(work, "AnalyzeAttachmentImage", in).Get(join, &result)
		return result, err
	}
	for base := 0; base < result.Chunks; base += in.Budget.Parallelism {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		var futures []workflow.Future
		for i := base; i < result.Chunks && i < base+in.Budget.Parallelism; i++ {
			futures = append(futures, workflow.ExecuteActivity(work, "AnalyzeDocumentChunk", chat.DocumentChunkInput{Analysis: in, Index: i}))
		}
		var first error
		for _, f := range futures {
			if err := f.Get(join, nil); err != nil && first == nil {
				first = err
				cancel()
			}
		}
		if first != nil {
			return result, first
		}
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	err := workflow.ExecuteActivity(work, "FinishDocumentAnalysis", in).Get(join, &result)
	return result, err
}
