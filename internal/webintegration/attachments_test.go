//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type attachmentInspectModel struct {
	mu       sync.Mutex
	requests []llm.Request
}

func (m *attachmentInspectModel) Chat(_ context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, r)
	if len(r.Tools) == 0 {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: r.Messages[len(r.Messages)-1].Content}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "Document outcome recorded"}}, nil
}
func (f *fixture) attachmentCommand(t *testing.T, c chat.Conversation, id chat.AttachmentID) chat.SessionCommand {
	in := chat.CommandInput{ConversationID: c.ID, RequestID: uuid.NewString(), Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: "explain the document", AttachmentIDs: []chat.AttachmentID{id}}}
	rc, e := f.repo.AcceptCommand(f.ctx, in)
	require.NoError(t, e)
	var seq int64
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT sequence FROM commands WHERE id=$1", rc.CommandID).Scan(&seq))
	return chat.SessionCommand{CommandID: rc.CommandID, ConversationID: c.ID, TurnID: rc.TurnID, RequestID: in.RequestID, Kind: in.Kind, Payload: in.Payload, PayloadDigest: rc.PayloadDigest, Sequence: seq, WorkflowID: c.WorkflowID, Model: c.Model}
}
func TestAttachmentApprovalExtractionAndRestart(t *testing.T) {
	f := setup(t)
	root := t.TempDir()
	store, e := attachments.NewStore(root, f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	c := f.conversation(t, "qwen3.5:9b")
	const content = "SECRET ORIGINAL CONTENT 8f7b2"
	file, e := store.Put(f.ctx, c.ID, strings.NewReader(content), attachments.UploadMetadata{Name: "untrusted.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	m := &attachmentInspectModel{}
	start := func() *worker.Runner {
		r, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store, WebTools: tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": activities.NewWebFetchAdapter(tools.NewFetcher()), "file_upload": activities.NewWebAttachmentAdapter(store)})})
		require.NoError(t, e)
		return r
	}
	runner := start()
	defer func() { runner.Close() }()
	command := f.attachmentCommand(t, c, file.ID)
	res, e := f.gateway.Submit(f.ctx, command)
	require.NoError(t, e)
	require.Equal(t, "accepted", res.State)
	var pending *chat.WebApproval
	require.Eventually(t, func() bool { pending = f.approvalState(t, c).PendingApproval; return pending != nil }, 10*time.Second, 20*time.Millisecond)
	m.mu.Lock()
	require.Empty(t, m.requests, "no inference before attachment approval")
	m.mu.Unlock()
	var status, text string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT extraction_status,extracted_text FROM attachments WHERE id=$1", file.ID).Scan(&status, &text))
	require.Equal(t, "pending", status)
	require.Empty(t, text)
	require.NoError(t, runner.Close())
	require.NoError(t, store.Close())
	store, e = attachments.NewStore(root, f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	require.NoError(t, store.Recover(f.ctx))
	runner = start()
	require.Equal(t, pending.ID, f.approvalState(t, c).PendingApproval.ID)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalDeny))
	require.NoError(t, e)
	f.completed(t, command)
	m.mu.Lock()
	require.Len(t, m.requests, 1)
	raw, _ := json.Marshal(m.requests[0])
	require.NotContains(t, string(raw), content)
	require.Contains(t, string(raw), "denied")
	m.mu.Unlock()
	command = f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, command)
	require.NoError(t, e)
	require.Eventually(t, func() bool { pending = f.approvalState(t, c).PendingApproval; return pending != nil }, 10*time.Second, 20*time.Millisecond)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalOnce))
	require.NoError(t, e)
	f.completed(t, command)
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT extraction_status,extracted_text FROM attachments WHERE id=$1", file.ID).Scan(&status, &text))
	require.Equal(t, "completed", status)
	require.Equal(t, content, text)
	m.mu.Lock()
	require.Len(t, m.requests, 3)
	raw, _ = json.Marshal(m.requests[1])
	require.Contains(t, string(raw), content, "approved child model request")
	raw, _ = json.Marshal(m.requests[2])
	require.Contains(t, string(raw), content, "only the approved persisted summary enters parent context")
	m.mu.Unlock()
	result, e := f.repo.WebToolResult(f.ctx, c.ID, pending.Scope.ToolCallID)
	require.NoError(t, e)
	require.Contains(t, result.Text, content)
	require.NotNil(t, result.Analysis)
	t.Log("real Temporal/SQL/worker/store restart: pending preserved, denied original absent from captured model request; authorized text persisted and enters actual request builder")
}
