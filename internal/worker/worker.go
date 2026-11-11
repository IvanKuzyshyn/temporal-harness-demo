// Package worker owns the Temporal connection and the three local task queues.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	temporalclient "go.temporal.io/sdk/client"
	temporallog "go.temporal.io/sdk/log"
	temporalworker "go.temporal.io/sdk/worker"
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

func selectModel(cfg config.Config) (llm.Model, error) {
	if cfg.Model == "mock" {
		return mock.New(), nil
	}
	host := strings.TrimSpace(cfg.OllamaHost)
	if host == "" {
		host = "127.0.0.1:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return ollama.New(host)
}

func start(ctx context.Context, cfg config.Config, dial func(context.Context, temporalclient.Options) (temporalclient.Client, error), factory func(temporalclient.Client, string, temporalworker.Options) temporalworker.Worker) (*Runner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	model, err := selectModel(cfg)
	if err != nil {
		return nil, err
	}
	c, err := dial(ctx, temporalclient.Options{HostPort: cfg.TemporalAddress, Logger: temporallog.NewStructuredLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))})
	if err != nil {
		return nil, err
	}
	r := &Runner{client: c, done: make(chan struct{})}
	a := &activities.Activities{Model: model}
	for _, queue := range []string{workflows.AgentsQueue, workflows.LLMQueue, workflows.SubagentsQueue} {
		if err = ctx.Err(); err != nil {
			_ = r.Close()
			return nil, err
		}
		opts := temporalworker.Options{}
		if queue == workflows.LLMQueue {
			opts.MaxConcurrentActivityExecutionSize = 1
		}
		w := factory(c, queue, opts)
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
		r.workers = append(r.workers, w)
		if err = w.Start(); err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("start %s worker: %w", queue, err)
		}
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Close()
		case <-r.done:
		}
	}()
	return r, nil
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
