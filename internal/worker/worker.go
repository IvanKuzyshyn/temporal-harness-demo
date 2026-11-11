// Package worker owns the Temporal connection and the three local task queues.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	temporalclient "go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
)

type Runner struct {
	client  temporalclient.Client
	workers []temporalworker.Worker
	once    sync.Once
	done    chan struct{}
}

func Start(ctx context.Context, cfg config.Config) (*Runner, error) {
	return start(ctx, cfg, temporalclient.DialContext, temporalworker.New)
}

func start(ctx context.Context, cfg config.Config, dial func(context.Context, temporalclient.Options) (temporalclient.Client, error), factory func(temporalclient.Client, string, temporalworker.Options) temporalworker.Worker) (*Runner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	model := selectModel(cfg)
	startup := &startupRPCContext{ctx: ctx}
	defer startup.finished.Store(true)
	c, err := dial(ctx, temporalclient.Options{
		HostPort: cfg.TemporalAddress,
		Logger:   temporallog.NewStructuredLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))),
		ConnectionOptions: temporalclient.ConnectionOptions{
			// WithUnaryInterceptor wraps the SDK's entire interceptor chain,
			// including retries, rather than just individual RPC attempts.
			DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(startup.intercept)},
		},
	})
	if err != nil {
		return nil, err
	}
	r := &Runner{client: c, done: make(chan struct{})}
	started := false
	defer func() {
		startup.finished.Store(true)
		if !started {
			_ = r.Close()
		}
	}()
	a := &activities.Activities{Model: model}
	for _, queue := range []string{workflows.AgentsQueue, workflows.LLMQueue, workflows.SubagentsQueue} {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		opts := temporalworker.Options{}
		if queue == workflows.LLMQueue {
			opts.MaxConcurrentActivityExecutionSize = 1
		}
		w := factory(c, queue, opts)
		r.workers = append(r.workers, w)
		switch queue {
		case workflows.AgentsQueue:
			w.RegisterWorkflow(workflows.SessionWorkflow)
			registerBasicActivities(w, a)
		case workflows.LLMQueue:
			w.RegisterActivity(a.LLMStream)
		case workflows.SubagentsQueue:
			w.RegisterWorkflow(workflows.ExplorerWorkflow)
			registerBasicActivities(w, a)
		}
		if err = w.Start(); err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			return nil, fmt.Errorf("start %s worker: %w", queue, err)
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	startup.finished.Store(true)
	started = true
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Close()
		case <-r.done:
		}
	}()
	return r, nil
}

// startupRPCContext bridges the background initialization contexts used by SDK
// v1.50's Worker.Start (ensureInitialized/loadCapabilities/loadNamespaceData).
// Each SDK Start finishes before any worker is stopped, so there is no racing
// Stop/Start or unowned goroutine that can create a worker later.
// Only initialization RPCs are bound. Polls, completions, heartbeats and shutdown
// RPCs retain their SDK contexts, even while other queues are still starting.
// After acquisition this interceptor is a no-op, including during graceful Stop.
// Recheck these call sites and interceptor ordering when upgrading the SDK.
type startupRPCContext struct {
	ctx      context.Context
	finished atomic.Bool
}

func (s *startupRPCContext) intercept(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if s.finished.Load() || (method != "/temporal.api.workflowservice.v1.WorkflowService/GetSystemInfo" &&
		method != "/temporal.api.workflowservice.v1.WorkflowService/DescribeNamespace") {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	// Keep SDK context values, cancellation and any shorter deadline.
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
	// A derived deadline can fire just before the parent's timer. Preserve a
	// standard context error even in that case (SDK service errors don't unwrap).
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func registerBasicActivities(w temporalworker.Worker, a *activities.Activities) {
	w.RegisterActivity(a.EmitEvent)
	w.RegisterActivity(a.AppendTranscript)
	w.RegisterActivity(a.ExecuteTool)
	w.RegisterActivity(a.GatherContext)
	w.RegisterActivity(a.NotifyApproval)
	w.RegisterActivity(a.RunCommand)
	w.RegisterActivity(a.PreflightTool)
	w.RegisterActivity(a.RecoverCommand)
}

// Close is idempotent and joins worker cleanup before closing the connection.
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
