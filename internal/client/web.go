package client

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
)

// WebGateway uses workflow identity, never a pinned run ID. It owns no model,
// database writer or agent loop. The caller owns the SDK client and observation loop.
type WebGateway struct {
	sdk                tc.Client
	reachable, polling atomic.Bool
}

func NewWebGateway(c tc.Client) *WebGateway { return &WebGateway{sdk: c} }
func (g *WebGateway) Submit(ctx context.Context, c dispatch.Command) (dispatch.CommandResult, error) {
	var result dispatch.CommandResult
	op := g.sdk.NewWithStartWorkflowOperation(tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue, WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ConversationID, Model: c.Model})
	h, err := g.sdk.UpdateWithStartWorkflow(ctx, tc.UpdateWithStartWorkflowOptions{StartWorkflowOperation: op, UpdateOptions: tc.UpdateWorkflowOptions{WorkflowID: c.WorkflowID, UpdateID: string(c.CommandID), UpdateName: string(c.Kind), Args: []any{c}, WaitForStage: tc.WorkflowUpdateStageCompleted}})
	if err != nil {
		return result, err
	}
	err = h.Get(ctx, &result)
	return result, err
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
