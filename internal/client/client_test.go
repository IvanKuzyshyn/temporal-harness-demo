package client

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

type sdkFake struct {
	temporalclient.Client
	start     temporalclient.StartWorkflowOptions
	startName any
	startArgs []any
	update    temporalclient.UpdateWithStartWorkflowOptions
	control   temporalclient.UpdateWorkflowOptions
	queryRun  string
	pages     int
	closed    int
	err       error
}

func (f *sdkFake) NewWithStartWorkflowOperation(o temporalclient.StartWorkflowOptions, w any, args ...any) temporalclient.WithStartWorkflowOperation {
	f.start = o
	f.startName = w
	f.startArgs = args
	return &startFake{}
}
func (f *sdkFake) UpdateWithStartWorkflow(_ context.Context, o temporalclient.UpdateWithStartWorkflowOptions) (temporalclient.WorkflowUpdateHandle, error) {
	f.update = o
	return &handleFake{}, f.err
}
func (f *sdkFake) UpdateWorkflow(_ context.Context, o temporalclient.UpdateWorkflowOptions) (temporalclient.WorkflowUpdateHandle, error) {
	f.control = o
	return &handleFake{}, f.err
}
func (f *sdkFake) QueryWorkflow(_ context.Context, id, run, name string, args ...any) (converter.EncodedValue, error) {
	f.queryRun = run
	return stateValue{}, f.err
}
func (f *sdkFake) Close() { f.closed++ }
func (f *sdkFake) ListWorkflow(_ context.Context, r *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	if r.Query != "WorkflowType = 'SessionWorkflow'" {
		panic(r.Query)
	}
	f.pages++
	if f.err != nil {
		return nil, f.err
	}
	if f.pages == 1 {
		return &workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{{Execution: &commonpb.WorkflowExecution{WorkflowId: "session-b"}}}, NextPageToken: []byte("next")}, nil
	}
	if string(r.NextPageToken) != "next" {
		panic("missing pagination token")
	}
	return &workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{{Execution: &commonpb.WorkflowExecution{WorkflowId: "session-a"}}, {Execution: &commonpb.WorkflowExecution{WorkflowId: "session-b"}}}}, nil
}

type stateValue struct{}

func (stateValue) HasValue() bool { return true }
func (stateValue) Get(v any) error {
	*v.(*workflows.State) = workflows.State{Status: "idle", Answer: "durable"}
	return nil
}

type startFake struct{}

func (*startFake) Get(context.Context) (temporalclient.WorkflowRun, error) {
	panic("must not wait for workflow")
}

type handleFake struct{}

func (*handleFake) WorkflowID() string { return "session" }
func (*handleFake) RunID() string      { return "must-not-cache" }
func (*handleFake) UpdateID() string   { return "id" }
func (*handleFake) Get(_ context.Context, v any) error {
	if p, ok := v.(*workflows.MessageReceipt); ok {
		*p = workflows.MessageReceipt{Disposition: "queued"}
	}
	return nil
}

func TestSubmitUsesPinnedUpdateWithStartAndRequestID(t *testing.T) {
	f := &sdkFake{}
	c := New(f)
	ctx := context.Background()
	in := workflows.Input{Model: "mock"}
	msg := workflows.MessageInput{Text: "hi", RequestID: "stable-request"}
	for range 2 {
		receipt, err := c.Submit(ctx, "session", in, msg)
		if err != nil || receipt.Disposition != "queued" {
			t.Fatalf("%+v %v", receipt, err)
		}
	}
	if f.start.ID != "session" || f.start.TaskQueue != workflows.AgentsQueue || f.start.WorkflowIDConflictPolicy != enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING || f.startName != workflows.SessionName {
		t.Fatalf("start=%+v name=%v", f.start, f.startName)
	}
	u := f.update.UpdateOptions
	if u.UpdateID != msg.RequestID || u.RunID != "" || u.WaitForStage != temporalclient.WorkflowUpdateStageCompleted || u.UpdateName != workflows.MessageUpdate || !reflect.DeepEqual(u.Args, []any{msg}) {
		t.Fatalf("update=%+v", u)
	}
	state, err := c.Query(ctx, "session")
	if err != nil || state.Answer != "durable" || f.queryRun != "" {
		t.Fatalf("query=%+v %v", state, err)
	}
	for _, call := range []func() error{func() error { return c.Stop(ctx, "session") }, func() error { return c.Continue(ctx, "session") }, func() error { return c.Decide(ctx, "session", workflows.DecisionInput{RequestID: "approval"}) }} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
		if f.control.RunID != "" || f.control.WaitForStage != temporalclient.WorkflowUpdateStageCompleted {
			t.Fatal(f.control)
		}
	}
	ids, err := c.Sessions(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"session-a", "session-b"}) || f.pages != 2 {
		t.Fatalf("sessions=%v %v", ids, err)
	}
	c.Close()
	c.Close()
	if f.closed != 1 {
		t.Fatal(f.closed)
	}
}
func TestClientPropagatesErrors(t *testing.T) {
	want := errors.New("offline")
	c := New(&sdkFake{err: want})
	ctx := context.Background()
	if _, err := c.Submit(ctx, "s", workflows.Input{}, workflows.MessageInput{Text: "hi"}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, "s"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := c.Sessions(ctx); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "s"); !errors.Is(err, want) {
		t.Fatal(err)
	}
}
