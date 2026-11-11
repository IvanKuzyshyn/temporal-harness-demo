package client

import (
	"context"
	"errors"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	tc "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

type closedClient struct {
	tc.Client
	status                 enumspb.WorkflowExecutionStatus
	submitErr, describeErr error
	queryErr               error
}

func (c closedClient) NewWithStartWorkflowOperation(tc.StartWorkflowOptions, interface{}, ...interface{}) tc.WithStartWorkflowOperation {
	return nil
}
func (c closedClient) UpdateWithStartWorkflow(context.Context, tc.UpdateWithStartWorkflowOptions) (tc.WorkflowUpdateHandle, error) {
	return nil, c.submitErr
}
func (c closedClient) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Status: c.status, Execution: &commonpb.WorkflowExecution{RunId: "closed-run"}}}, c.describeErr
}

type closedState struct{}

func (closedState) HasValue() bool { return true }
func (closedState) Get(v interface{}) error {
	*v.(*workflows.WebSessionState) = workflows.WebSessionState{}
	return nil
}
func (c closedClient) QueryWorkflow(context.Context, string, string, string, ...interface{}) (converter.EncodedValue, error) {
	return closedState{}, c.queryErr
}

type receiptReader struct {
	result chat.AdmissionResult
	err    error
	calls  int
}

func (r *receiptReader) CheckChatCommand(context.Context, chat.SessionCommand) (chat.AdmissionResult, error) {
	r.calls++
	return r.result, r.err
}
func (r *receiptReader) ResolveClosedChatCommand(_ context.Context, _ chat.SessionCommand, enqueued bool) (chat.AdmissionResult, error) {
	if enqueued {
		return chat.AdmissionResult{State: "accepted"}, nil
	}
	return chat.AdmissionResult{State: "rejected", Code: "conversation_closed"}, nil
}
func TestWebGatewayClosedResolutionKeepsAmbiguityAndReceipts(t *testing.T) {
	outage := errors.New("transport unavailable")
	for _, tt := range []struct {
		name                 string
		status               enumspb.WorkflowExecutionStatus
		describeErr, readErr error
		queryErr             error
		receipt              chat.AdmissionResult
		want                 chat.AdmissionResult
		wantErr              bool
		reads                int
	}{
		{name: "running transport outage", status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, wantErr: true},
		{name: "unknown transport", describeErr: outage, wantErr: true},
		{name: "continued run is not terminal", status: enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, wantErr: true},
		{name: "terminal query unavailable", status: enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, queryErr: outage, wantErr: true, reads: 1},
		{name: "closed new command", status: enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, want: chat.AdmissionResult{State: "rejected", Code: "conversation_closed"}, reads: 1},
		{name: "old durable receipt after rollover", status: enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, receipt: chat.AdmissionResult{State: "accepted"}, want: chat.AdmissionResult{State: "accepted"}, reads: 1},
		{name: "closed but receipt unavailable", status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, readErr: outage, wantErr: true, reads: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &receiptReader{result: tt.receipt, err: tt.readErr}
			g := NewWebGateway(closedClient{status: tt.status, describeErr: tt.describeErr, submitErr: outage, queryErr: tt.queryErr}, r)
			got, err := g.Submit(context.Background(), dispatch.Command{})
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.reads, r.calls)
		})
	}
}
