// Package client provides run-ID-independent access to durable sessions.
package client

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"
)

type Client struct {
	sdk  temporalclient.Client
	once sync.Once
}

func New(c temporalclient.Client) *Client { return &Client{sdk: c} }
func Dial(ctx context.Context, address string) (*Client, error) {
	c, err := temporalclient.DialContext(ctx, temporalclient.Options{HostPort: address, Logger: temporallog.NewStructuredLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))})
	if err != nil {
		return nil, err
	}
	return New(c), nil
}
func (c *Client) Close() { c.once.Do(func() { c.sdk.Close() }) }

// Submit waits for the enqueue receipt, not for a model response. Caller-provided
// request IDs become Temporal Update IDs and are never rewritten. If omitted,
// the SDK allocates one, but callers need to retain an ID to retry safely.
func (c *Client) Submit(ctx context.Context, id string, input workflows.Input, msg workflows.MessageInput) (workflows.MessageReceipt, error) {
	var receipt workflows.MessageReceipt
	if id == "" {
		return receipt, fmt.Errorf("session ID is required")
	}
	op := c.sdk.NewWithStartWorkflowOperation(temporalclient.StartWorkflowOptions{
		ID: id, TaskQueue: workflows.AgentsQueue,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, workflows.SessionName, input)
	handle, err := c.sdk.UpdateWithStartWorkflow(ctx, temporalclient.UpdateWithStartWorkflowOptions{
		StartWorkflowOperation: op,
		UpdateOptions:          temporalclient.UpdateWorkflowOptions{WorkflowID: id, UpdateID: msg.RequestID, UpdateName: workflows.MessageUpdate, Args: []any{msg}, WaitForStage: temporalclient.WorkflowUpdateStageCompleted},
	})
	if err != nil {
		return receipt, err
	}
	err = handle.Get(ctx, &receipt)
	return receipt, err
}
func (c *Client) Query(ctx context.Context, id string) (workflows.State, error) {
	var state workflows.State
	value, err := c.sdk.QueryWorkflow(ctx, id, "", workflows.StateQuery)
	if err != nil {
		return state, err
	}
	err = value.Get(&state)
	return state, err
}
func (c *Client) update(ctx context.Context, id, name string, args ...any) error {
	handle, err := c.sdk.UpdateWorkflow(ctx, temporalclient.UpdateWorkflowOptions{WorkflowID: id, UpdateName: name, Args: args, WaitForStage: temporalclient.WorkflowUpdateStageCompleted})
	if err != nil {
		return err
	}
	return handle.Get(ctx, nil)
}
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.update(ctx, id, workflows.StopUpdate)
}
func (c *Client) Continue(ctx context.Context, id string) error {
	return c.update(ctx, id, workflows.ContinueUpdate)
}
func (c *Client) Decide(ctx context.Context, id string, decision workflows.DecisionInput) error {
	return c.update(ctx, id, workflows.DecideUpdate, decision)
}
func (c *Client) Sessions(ctx context.Context) ([]string, error) {
	// Type filtering includes custom session IDs as well as the session-* prefix.
	// Leave Namespace empty: the pinned SDK fills its configured namespace.
	req := &workflowservice.ListWorkflowExecutionsRequest{Query: "WorkflowType = '" + workflows.SessionName + "'", PageSize: 100}
	seen := map[string]bool{}
	var ids []string
	for {
		page, err := c.sdk.ListWorkflow(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, execution := range page.Executions {
			id := execution.GetExecution().GetWorkflowId()
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if len(page.NextPageToken) == 0 {
			break
		}
		req.NextPageToken = page.NextPageToken
	}
	sort.Strings(ids)
	return ids, nil
}
