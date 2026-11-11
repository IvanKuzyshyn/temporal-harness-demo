package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
)

type fakeClient struct {
	temporalclient.Client
	closed int
}

func (c *fakeClient) Close() { c.closed++ }

type fakeWorker struct {
	temporalworker.Worker
	started, stopped      int
	fail                  bool
	workflows, activities []any
}

func (w *fakeWorker) RegisterWorkflow(v any) { w.workflows = append(w.workflows, v) }
func (w *fakeWorker) RegisterActivity(v any) { w.activities = append(w.activities, v) }
func (w *fakeWorker) Start() error {
	w.started++
	if w.fail {
		return errors.New("start failed")
	}
	return nil
}
func (w *fakeWorker) Stop() { w.stopped++ }

func TestWorkerQueuesLimitsAndCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "startup failure"}[fail], func(t *testing.T) {
			c := &fakeClient{}
			var workers []*fakeWorker
			var queues []string
			dial := func(ctx context.Context, o temporalclient.Options) (temporalclient.Client, error) {
				if o.Logger == nil || o.HostPort != "address" {
					t.Fatalf("options=%+v", o)
				}
				return c, nil
			}
			factory := func(_ temporalclient.Client, q string, o temporalworker.Options) temporalworker.Worker {
				queues = append(queues, q)
				if q == workflows.LLMQueue && o.MaxConcurrentActivityExecutionSize != 1 {
					t.Fatal(o.MaxConcurrentActivityExecutionSize)
				}
				w := &fakeWorker{fail: fail && q == workflows.LLMQueue}
				workers = append(workers, w)
				return w
			}
			r, err := start(context.Background(), config.Config{Model: "mock", TemporalAddress: "address"}, dial, factory)
			if fail {
				if err == nil || r != nil {
					t.Fatal("expected failure")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(queues) != 3 || queues[2] != workflows.SubagentsQueue {
					t.Fatal(queues)
				}
				if err = r.Close(); err != nil {
					t.Fatal(err)
				}
				_ = r.Close()
			}
			if c.closed != 1 {
				t.Fatalf("client closed %d", c.closed)
			}
			for _, w := range workers {
				if w.stopped != 1 {
					t.Fatalf("worker cleanup %+v", w)
				}
			}
		})
	}
}
func TestModelSelectionAndHostNormalization(t *testing.T) {
	for _, host := range []string{"127.0.0.1:11434", "http://localhost:11434", "https://localhost:11434", ""} {
		if _, err := selectModel(config.Config{Model: "gemma4:e4b", OllamaHost: host}); err != nil {
			t.Fatal(host, err)
		}
	}
	if _, err := selectModel(config.Config{Model: "mock", OllamaHost: ":bad"}); err != nil {
		t.Fatal(err)
	}
	if _, err := selectModel(config.Config{OllamaHost: "ftp://localhost"}); err == nil {
		t.Fatal("accepted invalid scheme")
	}
}
