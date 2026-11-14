//go:build integration

package webintegration_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

type contextModel struct {
	summaries atomic.Int32
	fail      atomic.Bool
	mu        sync.Mutex
	requests  []llm.Request
}

func (m *contextModel) Chat(_ context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, r)
	m.mu.Unlock()
	text := "The secret is blue kite."
	if strings.Contains(r.Messages[0].Content, "Summarize the following completed") {
		m.summaries.Add(1)
		if m.fail.Load() {
			return llm.Response{}, errors.New("injected summary inference failure")
		}
		text = "The user's secret is blue kite; source coverage is limited."
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: text}, Tokens: 20}, nil
}

type contextFaultRepository struct {
	*storage.Repository
	writes atomic.Int32
}

func (r *contextFaultRepository) CommitContextSummary(ctx context.Context, w chat.ContextSummaryWrite) (chat.Summary, error) {
	n := r.writes.Add(1)
	if n == 1 {
		return chat.Summary{}, errors.New("injected summary write failure")
	}
	result, e := r.Repository.CommitContextSummary(ctx, w)
	if e == nil && n == 3 {
		return result, errors.New("injected lost summary acknowledgement")
	}
	return result, e
}
func TestContextLongHistoryAndSummaryRetry(t *testing.T) {
	f := setup(t)
	m := &contextModel{}
	fault := &contextFaultRepository{Repository: f.repo}
	runner := f.start(t, fault, m)
	c := f.conversation(t, "qwen3.5:9b")
	for i := 0; i < 10; i++ {
		cmd := f.command(t, c, "Remember blue kite and answer briefly.")
		_, e := f.gateway.Submit(f.ctx, cmd)
		require.NoError(t, e)
		f.completed(t, cmd)
	}
	require.GreaterOrEqual(t, m.summaries.Load(), int32(3))
	require.GreaterOrEqual(t, fault.writes.Load(), int32(3))
	s, e := f.repo.LatestSummary(f.ctx, c.ID)
	require.NoError(t, e)
	require.Positive(t, s.CoveredSequence)
	var summaries, distinct int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*),count(DISTINCT covered_sequence) FROM conversation_summaries WHERE conversation_id=$1", c.ID).Scan(&summaries, &distinct))
	require.Equal(t, summaries, distinct)
	page, e := f.repo.ListMessages(f.ctx, c.ID, "", 100)
	require.NoError(t, e)
	require.Len(t, page.Messages, 20, "summarization must not delete originals")
	require.NotNil(t, page.Messages[19].Context)
	require.Positive(t, page.Messages[19].Context.Coverage.SummaryVersion)
	require.LessOrEqual(t, page.Messages[19].Context.Accounting.Total, 16384)
	var pending int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM chat_model_invocations WHERE conversation_id=$1 AND cleanup_confirmed_at IS NULL", c.ID).Scan(&pending))
	require.Zero(t, pending)
	runner.Close()
	f.start(t, f.repo, m)
	cmd := f.command(t, c, "What was my secret?")
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	f.completed(t, cmd)
	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.requests[len(m.requests)-1]
	joined := ""
	for _, msg := range last.Messages {
		joined += msg.Content
	}
	require.Contains(t, joined, "Lossy older conversation summary")
	require.Contains(t, joined, "blue kite")
}
func TestContextSummaryFailureReducedCoverage(t *testing.T) {
	f := setup(t)
	m := &contextModel{}
	m.fail.Store(true)
	f.start(t, f.repo, m)
	c := f.conversation(t, "gemma4:e4b")
	for i := 0; i < 8; i++ {
		cmd := f.command(t, c, "Remember blue kite.")
		_, e := f.gateway.Submit(f.ctx, cmd)
		require.NoError(t, e)
		f.completed(t, cmd)
	}
	require.Positive(t, m.summaries.Load())
	_, e := f.repo.LatestSummary(f.ctx, c.ID)
	require.ErrorIs(t, e, storage.ErrNotFound)
	page, e := f.repo.ListMessages(f.ctx, c.ID, "", 100)
	require.NoError(t, e)
	require.NotNil(t, page.Messages[15].Context)
	require.Equal(t, "generation_failed", page.Messages[15].Context.Coverage.SummaryFailure)
	require.Positive(t, page.Messages[15].Context.Coverage.OmittedExchanges)
	m.fail.Store(false)
	cmd := f.command(t, c, "Please remember.")
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	f.completed(t, cmd)
	_, e = f.repo.LatestSummary(f.ctx, c.ID)
	require.NoError(t, e)
}
