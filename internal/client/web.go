package client

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
)

// AdmissionRepository resolves permanent admission receipts, not turn completion.
// Terminal reconciliation serializes its receipt with outstanding activity writes.
type AdmissionRepository interface {
	CheckChatCommand(context.Context, chat.SessionCommand) (chat.AdmissionResult, error)
	ResolveClosedChatCommand(context.Context, chat.SessionCommand, bool) (chat.AdmissionResult, error)
}

// WebGateway submits by workflow identity, never a pinned run ID. Terminal
// reconciliation queries the exact closed run. It borrows the SDK client and
// admission repository; the caller owns connections and the observation loop.
type WebGateway struct {
	sdk                tc.Client
	receipts           AdmissionRepository
	reachable, polling atomic.Bool
}

func NewWebGateway(c tc.Client, receipts AdmissionRepository) *WebGateway {
	return &WebGateway{sdk: c, receipts: receipts}
}
func (g *WebGateway) Submit(ctx context.Context, c dispatch.Command) (dispatch.CommandResult, error) {
	var result dispatch.CommandResult
	op := g.sdk.NewWithStartWorkflowOperation(tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue, WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ConversationID, Model: c.Model})
	h, err := g.sdk.UpdateWithStartWorkflow(ctx, tc.UpdateWithStartWorkflowOptions{StartWorkflowOperation: op, UpdateOptions: tc.UpdateWorkflowOptions{WorkflowID: c.WorkflowID, UpdateID: string(c.CommandID), UpdateName: string(c.Kind), Args: []any{c}, WaitForStage: tc.WorkflowUpdateStageCompleted}})
	if err == nil {
		err = h.Get(ctx, &result)
	}
	if err == nil {
		return result, nil
	}
	return g.resolveClosed(ctx, c, err)
}

// A transport error alone is ambiguous. Only a confirmed terminal execution
// plus an authoritative receipt read can resolve admission. Receipts outlive
// individual runs, unlike Temporal's run-scoped Update results. Never restart
// a closed session or infer acceptance from a sequence watermark.
func (g *WebGateway) resolveClosed(ctx context.Context, c dispatch.Command, cause error) (dispatch.CommandResult, error) {
	var empty dispatch.CommandResult
	if ctx.Err() != nil || g.receipts == nil {
		return empty, cause
	}
	desc, err := g.sdk.DescribeWorkflowExecution(ctx, c.WorkflowID, "")
	if err != nil || desc == nil || desc.WorkflowExecutionInfo == nil {
		return empty, cause
	}
	switch desc.WorkflowExecutionInfo.Status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		result, err := g.receipts.CheckChatCommand(ctx, c)
		if err != nil {
			return empty, err
		}
		if result.State != "" {
			return result, nil
		}
		// A committed enqueue may precede its receipt indefinitely. Query the
		// pinned TERMINAL run, then reconcile against durable turn evidence.
		if desc.WorkflowExecutionInfo.Execution == nil || desc.WorkflowExecutionInfo.Execution.RunId == "" {
			return empty, cause
		}
		v, err := g.sdk.QueryWorkflow(ctx, c.WorkflowID, desc.WorkflowExecutionInfo.Execution.RunId, "state")
		if err != nil {
			return empty, err
		}
		var state workflows.WebSessionState
		if err = v.Get(&state); err != nil {
			return empty, err
		}
		enqueued := c.TurnID != "" && state.ActiveTurn == c.TurnID
		for _, ref := range state.Inbox {
			if ref.ConversationID == c.ConversationID && ref.TurnID == c.TurnID {
				enqueued = true
			}
		}
		return g.receipts.ResolveClosedChatCommand(ctx, c, enqueued)
	default:
		return empty, cause
	}
}
func (g *WebGateway) Connected() bool      { return g.reachable.Load() }
func (g *WebGateway) WorkersPolling() bool { return g.polling.Load() }

// ObserveHealth probes the actual transport and recent task-queue pollers. A
// server accepting queued work is NOT worker readiness or model availability.
// Pollers may be stale for <=90s; this is an observation, not an execution lease.
func (g *WebGateway) ObserveHealth(ctx context.Context) {
	defer g.reachable.Store(false)
	defer g.polling.Store(false)
	for {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := g.sdk.CheckHealth(probe, &tc.CheckHealthRequest{})
		g.reachable.Store(err == nil)
		ready := err == nil
		for _, q := range []struct {
			name string
			kind enumspb.TaskQueueType
		}{{workflows.WebAgentsQueue, enumspb.TASK_QUEUE_TYPE_WORKFLOW}, {workflows.WebAgentsQueue, enumspb.TASK_QUEUE_TYPE_ACTIVITY}, {workflows.WebLLMQueue, enumspb.TASK_QUEUE_TYPE_ACTIVITY}} {
			if !ready {
				break
			}
			desc, e := g.sdk.DescribeTaskQueue(probe, q.name, q.kind)
			found := false
			if e == nil {
				for _, p := range desc.Pollers {
					if p.LastAccessTime != nil && time.Since(p.LastAccessTime.AsTime()) < 90*time.Second {
						found = true
					}
				}
			}
			ready = found
		}
		g.polling.Store(ready)
		cancel()
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

var _ dispatch.SessionGateway = (*WebGateway)(nil)
