//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
)

type imageInspectModel struct{ images atomic.Int32 }

func (m *imageInspectModel) Chat(_ context.Context, req llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	for _, msg := range req.Messages {
		if len(msg.Images) > 0 {
			m.images.Add(1)
			return llm.Response{Message: llm.Message{Role: "assistant", Content: "Hello world from approved image"}}, nil
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "Image outcome recorded"}}, nil
}
func TestApprovedImageAnalysisAndNewInstruction(t *testing.T) {
	f := setup(t)
	store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	c := f.conversation(t, "qwen3.5:9b")
	pngData, e := os.ReadFile("../../scripts/testdata/bonjour.png")
	require.NoError(t, e)
	file, e := store.Put(f.ctx, c.ID, bytes.NewReader(pngData), attachments.UploadMetadata{Name: "hello.png", MediaType: "image/png"})
	require.NoError(t, e)
	m := &imageInspectModel{}
	runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store})
	require.NoError(t, e)
	defer runner.Close()
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	p := f.pending(t, c)
	require.Zero(t, m.images.Load())
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
	require.NoError(t, e)
	f.completed(t, cmd)
	result, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
	require.NoError(t, e)
	require.EqualValues(t, 1, m.images.Load(), "approved path must actually send image to selected model")
	require.Contains(t, result.Text, "Hello world")
	// Even identical bytes and cached result do not authorize a new action.
	next := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, next)
	require.NoError(t, e)
	second := f.pending(t, c)
	require.NotEqual(t, p.Scope.ActionHash, second.Scope.ActionHash)
	require.EqualValues(t, 1, m.images.Load())
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, second, chat.ApprovalDeny))
	require.NoError(t, e)
	f.completed(t, next)
	require.EqualValues(t, 1, m.images.Load())
}
