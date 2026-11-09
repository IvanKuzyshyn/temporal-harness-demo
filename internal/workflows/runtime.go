package workflows

import (
	"encoding/json"
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
	// Preserve the shared receiver fields above for independently owned seams.
	executionStartTurns int
	steerCounter        int
	deferringSteers     bool
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
		TaskQueue: AgentsQueue, StartToCloseTimeout: time.Minute, ScheduleToCloseTimeout: 3 * time.Minute, WaitForCancellation: true,
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

const maxRetainedChildren = 32
const maxQueryJSONBytes = maxInboxJSONBytes + (512 << 10)

// Call only at joined batch/run boundaries: the child seam may hold indexes
// into Children while its futures are live. At most 32 new children per batch
// plus 32 retained summaries can coexist.
func (r *runtime) pruneChildren() {
	if len(r.state.Children) > maxRetainedChildren {
		r.state.Children = append([]ChildState(nil), r.state.Children[len(r.state.Children)-maxRetainedChildren:]...)
	}
	for i := range r.state.Children {
		r.state.Children[i].WorkflowID = bounded(r.state.Children[i].WorkflowID, 256)
		r.state.Children[i].SummaryRef = bounded(r.state.Children[i].SummaryRef, 512)
		r.state.Children[i].Status = bounded(r.state.Children[i].Status, 32)
	}
}

// queryState projects display-only fields without mutating execution inputs,
// approval action bindings, or child indexes. JSON budgets include escaping.
func (r *runtime) queryState() State {
	s := r.state
	s.Answer = bounded(s.Answer, maxAnswerBytes)
	s.TranscriptRef = bounded(s.TranscriptRef, 2048)
	s.OriginInputID = bounded(s.OriginInputID, 512)
	if s.LastRunFailure != nil {
		f := *s.LastRunFailure
		f.Reason = bounded(f.Reason, 64)
		f.Detail = bounded(f.Detail, maxAnswerBytes)
		s.LastRunFailure = &f
	}
	s.Children = append([]ChildState(nil), s.Children...)
	if len(s.Children) > 2*maxRetainedChildren {
		s.Children = s.Children[len(s.Children)-2*maxRetainedChildren:]
	}
	for i := range s.Children {
		s.Children[i].WorkflowID = bounded(s.Children[i].WorkflowID, 256)
		s.Children[i].SummaryRef = bounded(s.Children[i].SummaryRef, 512)
		s.Children[i].Status = bounded(s.Children[i].Status, 32)
	}
	if s.PendingApproval != nil {
		p := *s.PendingApproval
		p.RequestID = bounded(p.RequestID, 256)
		p.ActionHash = bounded(p.ActionHash, 256)
		p.Preview = bounded(p.Preview, 8192)
		if b, err := json.Marshal(p.Call); err != nil || len(b) > 64<<10 {
			p.Call.Args = nil
		}
		p.Call.ID = bounded(p.Call.ID, 256)
		p.Call.Name = bounded(p.Call.Name, 256)
		if p.Decision != nil {
			d := *p.Decision
			d.RequestID = bounded(d.RequestID, 256)
			d.ActionHash = bounded(d.ActionHash, 256)
			d.User = bounded(d.User, 256)
			d.Reason = bounded(d.Reason, 8192)
			p.Decision = &d
		}
		s.PendingApproval = &p
	}
	if len(s.ActiveTools) > 32 {
		s.ActiveTools = s.ActiveTools[:32]
	}
	s.ActiveTools = append([]llm.ToolCall(nil), s.ActiveTools...)
	toolBytes := 2
	for i := range s.ActiveTools {
		call := &s.ActiveTools[i]
		call.ID = bounded(call.ID, 256)
		call.Name = bounded(call.Name, 256)
		b, err := json.Marshal(call)
		if err != nil || toolBytes+len(b)+1 > 64<<10 {
			call.Args = nil
			b, _ = json.Marshal(call)
		}
		toolBytes += len(b) + 1
	}
	// Receipts are bounded internally, but need not consume the public query
	// budget: clients only need their Completed receipt, not the retry ledger.
	s.Receipts = nil
	if b, err := json.Marshal(s); err != nil || len(b) > maxQueryJSONBytes {
		// The inbox itself has an exact 1 MiB budget. Drop only optional display
		// projections in the unlikely simultaneous worst-case escaped metadata.
		s.Children = nil
		s.ActiveTools = nil
		if s.PendingApproval != nil {
			s.PendingApproval.Call.Args = nil
			s.PendingApproval.Preview = ""
			s.PendingApproval.Decision = nil
		}
	}
	return s
}
