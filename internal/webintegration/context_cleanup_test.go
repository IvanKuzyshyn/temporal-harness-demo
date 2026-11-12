//go:build integration

package webintegration_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	tc "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	tw "go.temporal.io/sdk/worker"
)

type heldSummaryModel struct {
	count                                       atomic.Int32
	first, second, cancelled, release, returned chan struct{}
	unblock                                     func()
	mu                                          sync.Mutex
	follow                                      llm.Request
}

func (m *heldSummaryModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	if strings.Contains(r.Messages[0].Content, "Summarize the following completed") {
		n := m.count.Add(1)
		if n == 1 {
			close(m.first)
			<-ctx.Done()
			<-m.release
			close(m.returned)
			return llm.Response{}, ctx.Err()
		}
		if n == 2 {
			close(m.second)
			<-ctx.Done()
			close(m.cancelled)
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Completed earlier facts only."}, Tokens: 10}, nil
	}
	if r.Messages[len(r.Messages)-1].Content == "earlier queued follow-up" {
		m.mu.Lock()
		m.follow = r
		m.mu.Unlock()
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "replacement answer"}, Tokens: 10}, nil
}
func TestContextSummaryCancellationJoinsEveryBody(t *testing.T) {
	f := setup(t)
	c := f.conversation(t, "qwen3.5:9b")
	// Real durable completed history; the cancellation itself uses actual Temporal.
	for i := 0; i < 7; i++ {
		cmd := f.command(t, c, "old question")
		ref := chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
		require.NoError(t, f.repo.SaveChatModelResult(f.ctx, chat.ChatModelResult{TurnRef: ref, Text: "old answer"}))
		require.NoError(t, f.repo.PersistTurnResult(f.ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	}
	m := &heldSummaryModel{first: make(chan struct{}), second: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	m.unblock = sync.OnceFunc(func() { close(m.release) })
	a := &activities.ChatActivities{Repository: f.repo, Model: m}
	agents := tw.New(f.sdk, w.WebAgentsQueue, tw.Options{Interceptors: []interceptor.WorkerInterceptor{&attemptTimeouts{backoff: 100 * time.Millisecond}}})
	agents.RegisterWorkflow(w.WebChatSessionWorkflow)
	agents.RegisterActivity(a)
	require.NoError(t, agents.Start())
	defer agents.Stop()
	for i := 0; i < 2; i++ {
		worker := tw.New(f.sdk, w.WebLLMQueue, tw.Options{MaxConcurrentActivityExecutionSize: 1, WorkerStopTimeout: 5 * time.Second, MaxHeartbeatThrottleInterval: 100 * time.Millisecond, DefaultHeartbeatThrottleInterval: 100 * time.Millisecond})
		worker.RegisterActivity(a.ChatModel)
		require.NoError(t, worker.Start())
		defer worker.Stop()
	}
	defer m.unblock()
	run, e := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: w.WebAgentsQueue}, w.WebSessionName, w.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 1})
	require.NoError(t, e)
	target := f.command(t, c, "cancel this prompt")
	_, e = f.gateway.Submit(f.ctx, target)
	require.NoError(t, e)
	waitBarrier(t, m.first)
	follow := f.command(t, c, "earlier queued follow-up")
	_, e = f.gateway.Submit(f.ctx, follow)
	require.NoError(t, e)
	waitBarrier(t, m.second)
	replacement := f.control(t, c, target.TurnID, chat.CommandReplace, "replacement correction")
	result, e := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, e)
	require.Equal(t, "accepted", result.State)
	waitBarrier(t, m.cancelled)
	require.Eventually(t, func() bool {
		var pending, acked int
		e := f.pool.QueryRow(f.ctx, "SELECT count(*) FILTER(WHERE cleanup_confirmed_at IS NULL),count(*) FILTER(WHERE cleanup_confirmed_at IS NOT NULL) FROM chat_model_invocations WHERE turn_id=$1", target.TurnID).Scan(&pending, &acked)
		return e == nil && pending == 1 && acked == 1
	}, 5*time.Second, 20*time.Millisecond)
	turn, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: replacement.TurnID})
	require.NoError(t, e)
	require.Equal(t, "queued", turn.Status)
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM conversation_summaries WHERE conversation_id=$1", c.ID).Scan(&n))
	require.Zero(t, n, "cancelled summary is not durable memory")
	m.unblock()
	waitBarrier(t, m.returned)
	f.completed(t, replacement)
	f.completed(t, follow)
	m.mu.Lock()
	defer m.mu.Unlock()
	text := ""
	for _, msg := range m.follow.Messages {
		text += msg.Content + "\n"
	}
	require.Contains(t, text, "replacement correction")
	require.Contains(t, text, "cancel this prompt")
	require.Contains(t, text, "replacement answer")
	description, e := f.sdk.DescribeWorkflowExecution(f.ctx, c.WorkflowID, "")
	require.NoError(t, e)
	require.NotEqual(t, run.GetRunID(), description.WorkflowExecutionInfo.Execution.RunId, "summary boundaries survive Continue-As-New")
}
