package workflows

import (
	"fmt"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func (r *runtime) registerApprovalHandlers(ctx workflow.Context) error {
	return workflow.SetUpdateHandlerWithOptions(ctx, DecideUpdate, r.decide, workflow.UpdateHandlerOptions{Validator: r.validateDecision})
}

func (r *runtime) validateDecision(ctx workflow.Context, d DecisionInput) error {
	p := r.state.PendingApproval
	if p == nil || p.RequestID != d.RequestID {
		return fmt.Errorf("unknown or stale approval request")
	}
	if p.Decision != nil {
		return fmt.Errorf("approval decision already recorded")
	}
	if !workflow.Now(ctx).Before(p.ExpiresAt) {
		return fmt.Errorf("stale approval request: deadline passed")
	}
	if p.ActionHash != d.ActionHash {
		return fmt.Errorf("approval action hash mismatch")
	}
	if strings.TrimSpace(d.User) == "" {
		return fmt.Errorf("approval user is required")
	}
	if len(d.User) > 256 || len(d.Reason) > 4096 {
		return fmt.Errorf("approval user or reason too large")
	}
	return nil
}

func (r *runtime) decide(ctx workflow.Context, d DecisionInput) (string, error) {
	// Recheck without yielding: first decision wins even if two Updates were
	// validated before their handlers run. Same UpdateID is server-idempotent.
	if err := r.validateDecision(ctx, d); err != nil {
		return "", err
	}
	// User is a local display identity. Real authentication/authorization belongs
	// at the caller/service boundary, not in this demonstration workflow.
	r.state.PendingApproval.Decision = &d
	return "recorded", nil
}

// approveTool uses only a pure action hash in workflow code. All filesystem
// validation and preview generation belong to the preflight activity.
func (r *runtime) approveTool(ctx workflow.Context, call llm.ToolCall) (bool, error) {
	hash, err := tools.ActionHash(call)
	if err != nil {
		return false, fmt.Errorf("tool approval denied: %w", err)
	}
	var preflight PreflightResult
	err = workflow.ExecuteActivity(activityContext(ctx), PreflightActivity, ToolInput{InputID: r.inputID, Scope: r.scope, DataDir: r.cfg.DataDir, Workdir: r.cfg.Workdir, Call: call}).Get(ctx, &preflight)
	if err != nil {
		return false, err
	}
	if !preflight.Allowed {
		return false, fmt.Errorf("tool approval denied: %s", preflight.Reason)
	}
	if preflight.ActionHash != hash {
		return false, fmt.Errorf("tool approval denied: preflight action hash mismatch")
	}
	timeout := r.cfg.ApprovalTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	r.state.ApprovalCounter++
	p := &PendingApproval{RequestID: fmt.Sprintf("%s-approval-%d", workflow.GetInfo(ctx).WorkflowExecution.ID, r.state.ApprovalCounter), ActionHash: hash, Call: call, ExpiresAt: workflow.Now(ctx).Add(timeout), Preview: bounded(preflight.Preview, 8192)}
	r.state.PendingApproval = p
	r.state.Status = "blocked"
	previousPhase := r.state.Phase
	r.phase(ctx, "awaiting approval")
	defer func() { r.state.PendingApproval = nil; r.state.Status = "working"; r.phase(ctx, previousPhase) }()
	data := events.ApprovalData{RequestID: p.RequestID, ActionHash: hash, Command: bounded(call.Name+" "+string(call.Args), 8192)}
	// Display must never delay the approval wait, extend its deadline or decide
	// policy. One bounded best-effort attempt; no wait on the display sidechannel.
	displayCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: AgentsQueue, StartToCloseTimeout: 5 * time.Second, ScheduleToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	workflow.ExecuteActivity(displayCtx, NotifyActivity, EmitInput{Scope: r.scope, DataDir: r.cfg.DataDir, Type: events.ApprovalRequested, Pattern: events.Approval, Data: data})
	ok, waitErr := workflow.AwaitWithTimeout(ctx, timeout, func() bool { return p.Decision != nil })
	approved := false
	switch {
	case waitErr != nil:
		data.Decision = "cancel"
		data.Reason = "approval canceled"
		err = waitErr
	case !ok:
		data.Decision = "timeout"
		data.Reason = "approval timed out"
		err = fmt.Errorf("approval timed out")
	default:
		d := p.Decision
		data.User = d.User
		data.Reason = d.Reason
		if d.Approved {
			data.Decision = "approve"
			approved = true
		} else {
			data.Decision = "deny"
			err = fmt.Errorf("denied by %s: %s", d.User, d.Reason)
		}
	}
	// A canceled run still gets a bounded best-effort resolution notification.
	resolvedCtx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	resolvedCtx = workflow.WithActivityOptions(resolvedCtx, workflow.ActivityOptions{TaskQueue: AgentsQueue, StartToCloseTimeout: 5 * time.Second, ScheduleToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	var seq uint64
	if emitErr := workflow.ExecuteActivity(resolvedCtx, EmitActivity, EmitInput{Scope: r.scope, DataDir: r.cfg.DataDir, Type: events.ApprovalResolved, Pattern: events.Approval, Data: data}).Get(resolvedCtx, &seq); emitErr == nil {
		r.recordSeq(seq)
	}
	// Stop racing the resolution display must not release an unsafe tool.
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return approved, err
}
