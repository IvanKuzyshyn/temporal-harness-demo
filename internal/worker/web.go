package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	tc "go.temporal.io/sdk/client"
	tw "go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
)

// StartWeb owns its Temporal client/workers, but borrows the DB/model activities.
// documents queue is reserved, deliberately not falsely advertised as polling.
func StartWeb(ctx context.Context, address string, a *activities.ChatActivities) (*Runner, error) {
	return startWeb(ctx, address, a, tc.DialContext, tw.New)
}
func startWeb(ctx context.Context, address string, a *activities.ChatActivities, dial func(context.Context, tc.Options) (tc.Client, error), factory func(tc.Client, string, tw.Options) tw.Worker) (*Runner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	startup := &startupRPCContext{ctx: ctx}
	defer startup.finished.Store(true)
	c, err := dial(ctx, tc.Options{HostPort: address, ConnectionOptions: tc.ConnectionOptions{DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(startup.intercept)}}})
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
	for _, q := range []string{workflows.WebAgentsQueue, workflows.WebLLMQueue} {
		opts := tw.Options{WorkerStopTimeout: 10 * time.Second}
		if q == workflows.WebLLMQueue {
			opts.MaxConcurrentActivityExecutionSize = 1
		}
		w := factory(c, q, opts)
		r.workers = append(r.workers, w)
		if q == workflows.WebAgentsQueue {
			w.RegisterWorkflow(workflows.WebChatSessionWorkflow)
			w.RegisterActivity(a.CheckChatCommand)
			w.RegisterActivity(a.ResumeChatAdmission)
			w.RegisterActivity(a.RecordChatAdmission)
			w.RegisterActivity(a.StartChatTurn)
			w.RegisterActivity(a.MarkChatCancelling)
			w.RegisterActivity(a.PersistTurnResult)
		} else {
			w.RegisterActivity(a.ChatModel)
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = w.Start(); err != nil {
			return nil, fmt.Errorf("start web worker: %w", err)
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
