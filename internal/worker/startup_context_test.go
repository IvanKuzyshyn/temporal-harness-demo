package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestStartupRPCContextScope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, finished := range []bool{false, true} {
		for _, method := range []string{"GetSystemInfo", "DescribeNamespace", "PollWorkflowTaskQueue", "RespondWorkflowTaskCompleted", "RecordActivityTaskHeartbeat", "ShutdownWorker"} {
			t.Run(method+map[bool]string{false: "/starting", true: "/running"}[finished], func(t *testing.T) {
				s := &startupRPCContext{ctx: ctx}
				s.finished.Store(finished)
				called := false
				invoke := func(rpcCtx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
					called = true
					return rpcCtx.Err()
				}
				err := s.intercept(context.Background(), "/temporal.api.workflowservice.v1.WorkflowService/"+method, nil, nil, nil, invoke)
				bound := !finished && (method == "GetSystemInfo" || method == "DescribeNamespace")
				if bound {
					if called || !errors.Is(err, context.Canceled) {
						t.Fatalf("initialization escaped cancellation: called=%v err=%v", called, err)
					}
				} else if !called || err != nil {
					t.Fatalf("non-startup RPC was canceled: called=%v err=%v", called, err)
				}
			})
		}
	}
}

func TestStartupRPCContextPreservesSDKContext(t *testing.T) {
	type key struct{}
	for _, sdkEarlier := range []bool{false, true} {
		startupDeadline := time.Now().Add(time.Minute)
		sdkDeadline := startupDeadline.Add(time.Minute)
		wantDeadline := startupDeadline
		if sdkEarlier {
			sdkDeadline = startupDeadline.Add(-30 * time.Second)
			wantDeadline = sdkDeadline
		}
		startupCtx, cancelStartup := context.WithDeadline(context.Background(), startupDeadline)
		sdkCtx, cancelSDK := context.WithDeadline(context.WithValue(context.Background(), key{}, "sdk value"), sdkDeadline)
		s := &startupRPCContext{ctx: startupCtx}
		invoke := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
				t.Errorf("deadline=%v, want %v", deadline, wantDeadline)
			}
			if ctx.Value(key{}) != "sdk value" {
				t.Error("lost SDK context values")
			}
			cancelSDK()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Error("lost SDK cancellation")
			}
			return ctx.Err()
		}
		err := s.intercept(sdkCtx, "/temporal.api.workflowservice.v1.WorkflowService/DescribeNamespace", nil, nil, nil, invoke)
		cancelSDK()
		cancelStartup()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	}
}
