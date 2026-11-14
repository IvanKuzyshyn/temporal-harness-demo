//go:build integration

package webintegration_test

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type restartDocumentModel struct {
	once    atomic.Bool
	started chan struct{}
}

func (m *restartDocumentModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	if len(r.Tools) == 0 && !m.once.Swap(true) {
		close(m.started)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "durable document summary"}}, nil
}
func TestDocumentWorkerRestartAndFollowUp(t *testing.T) {
	f := setup(t)
	root := t.TempDir()
	store, e := attachments.NewStore(root, f.repo, nil)
	require.NoError(t, e)
	defer func() { store.Close() }()
	c := f.conversation(t, "qwen3.5:9b")
	file, e := store.Put(f.ctx, c.ID, strings.NewReader("restart document"), attachments.UploadMetadata{Name: "restart.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	m := &restartDocumentModel{started: make(chan struct{})}
	start := func() *worker.Runner {
		r, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store})
		require.NoError(t, e)
		return r
	}
	runner := start()
	defer func() { runner.Close() }()
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	p := f.pending(t, c)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
	require.NoError(t, e)
	waitBarrier(t, m.started)
	require.NoError(t, runner.Close())
	require.NoError(t, store.Close())
	store, e = attachments.NewStore(root, f.repo, nil)
	require.NoError(t, e)
	require.NoError(t, store.Recover(f.ctx))
	runner = start()
	require.Eventually(t, func() bool {
		turn, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
		return e == nil && turn.Status == "completed"
	}, 20*time.Second, 50*time.Millisecond)
	result, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
	require.NoError(t, e)
	require.Contains(t, result.Text, "durable document summary")
	follow := f.command(t, c, "what did you learn?")
	_, e = f.gateway.Submit(f.ctx, follow)
	require.NoError(t, e)
	f.completed(t, follow)
	var outstanding int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL", cmd.TurnID).Scan(&outstanding))
	require.Zero(t, outstanding)
}
