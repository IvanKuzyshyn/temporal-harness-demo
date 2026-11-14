//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	tw "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type toolTimeouts struct {
	interceptor.WorkerInterceptorBase
}
type toolInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}
type toolOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

func (i *toolTimeouts) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &toolInbound{interceptor.WorkflowInboundInterceptorBase{Next: next}}
}
func (i *toolInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&toolOutbound{interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}
func (i *toolOutbound) ExecuteActivity(ctx workflow.Context, name string, args ...interface{}) workflow.Future {
	if name == w.WebToolActivity {
		opts := workflow.GetActivityOptions(ctx)
		opts.StartToCloseTimeout = 4 * time.Second
		opts.HeartbeatTimeout = time.Second
		retry := *opts.RetryPolicy
		retry.InitialInterval = 100 * time.Millisecond
		opts.RetryPolicy = &retry
		ctx = workflow.WithActivityOptions(ctx, opts)
	}
	return i.Next.ExecuteActivity(ctx, name, args...)
}

type oneToolModel struct{}

func (oneToolModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	for _, m := range r.Messages {
		if m.Role == "tool" {
			return llm.Response{Message: llm.Message{Role: "assistant", Content: "recorded result"}}, nil
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{Name: "web_fetch", Args: json.RawMessage(`{"url":"https://example.com/held"}`)}}}}, nil
}
func TestWebToolTimedOutBodyBlocksAcceptedReplacement(t *testing.T) {
	f := setup(t)
	started, second, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	registry := tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": func(ctx context.Context, _ chat.ApprovalScope, _ json.RawMessage) (chat.WebToolResult, error) {
		if activity.GetInfo(ctx).Attempt == 1 {
			close(started)
			<-ctx.Done()
			<-release
			close(returned)
			return chat.WebToolResult{}, ctx.Err()
		}
		close(second)
		<-ctx.Done()
		return chat.WebToolResult{}, ctx.Err()
	}})
	a := &activities.ChatActivities{Repository: f.repo, Model: oneToolModel{}, WebTools: registry}
	startAgents := func() tw.Worker {
		agents := tw.New(f.sdk, w.WebAgentsQueue, tw.Options{Interceptors: []interceptor.WorkerInterceptor{&toolTimeouts{}}, MaxHeartbeatThrottleInterval: 100 * time.Millisecond, DefaultHeartbeatThrottleInterval: 100 * time.Millisecond})
		agents.RegisterWorkflow(w.WebChatSessionWorkflow)
		agents.RegisterActivity(a)
		require.NoError(t, agents.Start())
		return agents
	}
	agents := startAgents()
	t.Cleanup(func() { agents.Stop() })
	models := tw.New(f.sdk, w.WebLLMQueue, tw.Options{})
	models.RegisterActivityWithOptions(a.ChatStep, activity.RegisterOptions{Name: w.WebModelActivity})
	require.NoError(t, models.Start())
	t.Cleanup(models.Stop)
	t.Cleanup(func() {
		unblock()
		select {
		case <-started:
			waitBarrier(t, returned)
		default:
		}
	})
	c := f.conversation(t, "qwen3.5:9b")
	first := f.command(t, c, "held tool")
	_, err := f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	approval := f.pending(t, c)
	decision := f.approvalCommand(t, c, approval, chat.ApprovalOnce)
	_, err = f.gateway.Submit(f.ctx, decision)
	require.NoError(t, err)
	waitBarrier(t, started)
	waitBarrier(t, second)
	replacement := f.control(t, c, first.TurnID, chat.CommandReplace, "must join older tool invocation")
	result, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, "accepted", result.State)
	require.Eventually(t, func() bool { return f.approvalState(t, c).Status == "cancelling_interrupted" }, 12*time.Second, 50*time.Millisecond)
	var missing int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL`, first.TurnID).Scan(&missing))
	require.Equal(t, 1, missing)
	target, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: first.TurnID})
	require.NoError(t, err)
	require.Equal(t, "cancelling", target.Status)
	next, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: replacement.TurnID})
	require.NoError(t, err)
	require.Equal(t, "queued", next.Status)
	// Restart workflow poller while the old body is still held. Stop only joins
	// SDK polling here; the ledger, not that Stop return, is cleanup evidence.
	agents.Stop()
	agents = startAgents()
	require.Equal(t, "cancelling_interrupted", f.approvalState(t, c).Status)
}
