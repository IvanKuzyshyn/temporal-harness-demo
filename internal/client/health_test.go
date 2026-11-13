package client

import (
	"context"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	tc "go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type healthClient struct {
	tc.Client
	missing enumspb.TaskQueueType
}

func (c *healthClient) CheckHealth(context.Context, *tc.CheckHealthRequest) (*tc.CheckHealthResponse, error) {
	return &tc.CheckHealthResponse{}, nil
}
func (c *healthClient) DescribeTaskQueue(_ context.Context, name string, kind enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	if name == workflows.WebDocumentsQueue && kind == c.missing {
		return &workflowservice.DescribeTaskQueueResponse{}, nil
	}
	return &workflowservice.DescribeTaskQueueResponse{Pollers: []*taskqueuepb.PollerInfo{{LastAccessTime: timestamppb.Now()}}}, nil
}
func TestReadinessRejectsMissingDocumentWorkflowOrActivityPoller(t *testing.T) {
	for _, kind := range []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY, enumspb.TASK_QUEUE_TYPE_UNSPECIFIED} {
		t.Run(kind.String(), func(t *testing.T) {
			c := &healthClient{missing: kind}
			g := NewWebGateway(c, nil)
			g.probeHealth(context.Background())
			if !g.Connected() {
				t.Fatal("healthy Temporal connection rejected")
			}
			if g.WorkersPolling() != (kind == enumspb.TASK_QUEUE_TYPE_UNSPECIFIED) {
				t.Fatalf("readiness=%v with missing document poller %v", g.WorkersPolling(), kind)
			}
		})
	}
}
