// Package worker owns the web runtime's Temporal connection and task queues.
package worker

import (
	"context"
	"sync"
	"sync/atomic"

	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
)

type Runner struct {
	client  temporalclient.Client
	workers []temporalworker.Worker
	once    sync.Once
	done    chan struct{}
}

// startupRPCContext bridges SDK initialization only. Polls, completions,
// heartbeats and shutdown RPCs retain their SDK contexts. Recheck SDK call sites
// and interceptor ordering when upgrading; runtime cancellation is not RPC death.
type startupRPCContext struct {
	ctx      context.Context
	finished atomic.Bool
}

func (s *startupRPCContext) intercept(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if s.finished.Load() || (method != "/temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo" && method != "/temporal.api.workflowservice.v1.WorkflowService/DescribeNamespace") {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	var cancel context.CancelFunc
	if deadline, ok := s.ctx.Deadline(); ok {
		ctx, cancel = context.WithDeadline(ctx, deadline)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	err := invoker(ctx, method, req, reply, cc, opts...)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Close joins workers then closes the local client once. done proves local
// cleanup, not remote ConnEnd observation or provider/GPU resource reclamation.
func (r *Runner) Close() error {
	r.once.Do(func() {
		for i := len(r.workers) - 1; i >= 0; i-- {
			r.workers[i].Stop()
		}
		r.client.Close()
		close(r.done)
	})
	return nil
}
