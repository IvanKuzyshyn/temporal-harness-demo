package activities

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// Observe real SDK RecordHeartbeat calls before SDK RPC batching, forwarding
// every call. Display events never increment this counter.
type commandHeartbeatObserver struct {
	interceptor.WorkerInterceptorBase
	requests *atomic.Int32
}

func (o *commandHeartbeatObserver) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &commandHeartbeatInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, requests: o.requests}
}

type commandHeartbeatInbound struct {
	interceptor.ActivityInboundInterceptorBase
	requests *atomic.Int32
}

func (o *commandHeartbeatInbound) Init(next interceptor.ActivityOutboundInterceptor) error {
	return o.Next.Init(&commandHeartbeatOutbound{ActivityOutboundInterceptorBase: interceptor.ActivityOutboundInterceptorBase{Next: next}, requests: o.requests})
}

type commandHeartbeatOutbound struct {
	interceptor.ActivityOutboundInterceptorBase
	requests *atomic.Int32
}

func (o *commandHeartbeatOutbound) RecordHeartbeat(ctx context.Context, details ...any) {
	o.requests.Add(1)
	o.Next.RecordHeartbeat(ctx, details...)
}

func TestCommandDisplayBacklogBoundedWithFinalHeartbeat(t *testing.T) {
	in := commandInput(t, "cat", "large")
	text := strings.Repeat("large output\n", 1<<20)
	if err := os.WriteFile(filepath.Join(in.Workdir, "large"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	var requests atomic.Int32
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&commandHeartbeatObserver{requests: &requests}}})
	a := &Activities{}
	calls := 0
	run := func(ctx context.Context, input workflows.ToolInput) (workflows.CommandResult, error) {
		return a.runCommand(ctx, input, func(ctx context.Context, input workflows.EmitInput, seq *uint64) error {
			calls++
			// A slow but functioning display. The command finishes well before this
			// returns; final display must remain heartbeated and not drain megabytes.
			before := requests.Load()
			time.Sleep(2200 * time.Millisecond)
			if requests.Load() <= before {
				t.Error("display/finalization stopped actual heartbeats")
			}
			return a.emitDisplay(ctx, input, seq)
		})
	}
	env.RegisterActivity(run)
	started := time.Now()
	value, err := env.ExecuteActivity(run, in)
	if err != nil {
		t.Fatal(err)
	}
	var result workflows.CommandResult
	if err := value.Get(&result); err != nil {
		t.Fatal(err)
	}
	if calls > 3 || time.Since(started) > 8*time.Second {
		t.Fatalf("unbounded display: %d calls", calls)
	}
	if result.ExitCode != 0 || !result.Truncated {
		t.Fatalf("%+v", result)
	}
	full, err := os.ReadFile(filepath.Join(in.DataDir, result.LogRef))
	if err != nil || string(full) != text {
		t.Fatalf("authoritative log lost: %v", err)
	}
	// The activity joins its heartbeat goroutine rather than leaving it running.
	before := requests.Load()
	time.Sleep(2100 * time.Millisecond)
	if requests.Load() != before {
		t.Fatal("heartbeat goroutine survived return")
	}
}
