package workflows

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// runtime is the shared receiver for the independently owned seam files. All
// fields are workflow-local, deterministic state; no model/OS client belongs here.
type runtime struct {
	cfg     Input
	state   State
	scope   events.Scope
	cancel  workflow.CancelFunc
	inputID string
}

const maxAnswerBytes = 8192

func bounded(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

func activityContext(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: AgentsQueue, StartToCloseTimeout: time.Minute, ScheduleToCloseTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3},
	})
}
func (r *runtime) emit(ctx workflow.Context, typ events.Type, pattern events.Pattern, data any) error {
	var seq uint64
	err := workflow.ExecuteActivity(activityContext(ctx), EmitActivity, EmitInput{Scope: r.scope, DataDir: r.cfg.DataDir, Type: typ, Pattern: pattern, Data: data}).Get(ctx, &seq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if temporal.IsCanceledError(err) {
			return err
		}
		workflow.GetLogger(ctx).Warn("Display emission failed", "type", typ, "error", err)
		return nil
	}
	r.recordSeq(seq)
	return nil
}
func (r *runtime) recordSeq(seq uint64) {
	if seq > r.state.LastSeq {
		r.state.LastSeq = seq
	}
}
func (r *runtime) appendMessages(ctx workflow.Context, key string, messages ...llm.Message) error {
	var res AppendResult
	err := workflow.ExecuteActivity(activityContext(ctx), TranscriptActivity, AppendInput{Scope: r.scope, DataDir: r.cfg.DataDir, TranscriptRef: r.state.TranscriptRef, EntryID: key, Messages: messages}).Get(ctx, &res)
	if err == nil {
		r.state.TranscriptRef = res.TranscriptRef
		r.recordSeq(res.LastSeq)
	}
	return err
}
func (r *runtime) fail(reason string, err error) {
	r.state.LastRunFailure = &Failure{Reason: reason, Detail: bounded(fmt.Sprint(err), maxAnswerBytes)}
}
