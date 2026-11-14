//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
)

type contradictingContextModel struct {
	mu       sync.Mutex
	requests []llm.Request
	analyses int
}

func (m *contradictingContextModel) Chat(_ context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, r)
	text := "Assistant interpretation: no observation was recorded."
	if !strings.Contains(r.Messages[0].Content, "Summarize the following completed") && len(r.Tools) == 0 {
		m.analyses++
		text = "Recorded observation: amber."
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: text}, Tokens: 12}, nil
}
func TestContextCanonicalEvidenceSurvivesForgetfulSummaryAndStoreRestart(t *testing.T) {
	f := setup(t)
	root := t.TempDir()
	store, e := attachments.NewStore(root, f.repo, nil)
	require.NoError(t, e)
	defer func() { store.Close() }()
	m := &contradictingContextModel{}
	start := func() *worker.Runner {
		r, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store})
		require.NoError(t, e)
		return r
	}
	runner := start()
	defer func() { runner.Close() }()
	c := f.conversation(t, "gemma4:e4b")
	approved, e := store.Put(f.ctx, c.ID, strings.NewReader("Document containing an amber observation."), attachments.UploadMetadata{Name: "source.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	denied, e := store.Put(f.ctx, c.ID, strings.NewReader("UNAPPROVED SECRET MUST NOT ENTER MEMORY"), attachments.UploadMetadata{Name: "no-access.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	var approvedScope chat.ApprovalScope
	for i, att := range []chat.Attachment{approved, denied} {
		cmd := f.attachmentCommand(t, c, att.ID)
		_, e = f.gateway.Submit(f.ctx, cmd)
		require.NoError(t, e)
		p := f.pending(t, c)
		decision := chat.ApprovalDeny
		if i == 0 {
			decision = chat.ApprovalOnce
			approvedScope = p.Scope
		}
		_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, decision))
		require.NoError(t, e)
		f.completed(t, cmd)
	}
	for i := 0; i < 7; i++ {
		cmd := f.command(t, c, fmt.Sprintf("filler %d", i))
		_, e = f.gateway.Submit(f.ctx, cmd)
		require.NoError(t, e)
		f.completed(t, cmd)
	}
	s, e := f.repo.LatestSummary(f.ctx, c.ID)
	require.NoError(t, e)
	require.Contains(t, s.Text, "no observation")
	require.NoError(t, runner.Close())
	var originalUserID string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT entry_id::text FROM messages WHERE turn_id=$1 AND role='user'", approvedScope.TurnID).Scan(&originalUserID))
	require.NoError(t, store.Close())
	// Only this fixture's own original is moved. Any hidden re-open would fail;
	// the recorded output must suffice even though the original is unavailable.
	require.Equal(t, string(approved.ID)+"/original", approved.StorageKey)
	path := filepath.Join(root, approved.StorageKey)
	require.NoError(t, os.Rename(path, path+".held"))
	store, e = attachments.NewStore(root, f.repo, nil)
	require.NoError(t, e)
	runner = start()
	cmd := f.command(t, c, "Recall the recorded observation without reading originals.")
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	f.completed(t, cmd)
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Equal(t, 1, m.analyses, "no fresh analysis or original re-read")
	last := m.requests[len(m.requests)-1]
	raw, _ := json.Marshal(last)
	require.Contains(t, string(raw), "Recorded observation: amber.")
	require.Contains(t, string(raw), "no observation was recorded")
	require.Contains(t, string(raw), approvedScope.ToolCallID)
	require.Contains(t, string(raw), approved.Checksum)
	require.Contains(t, string(raw), "explain the document")
	require.Equal(t, 1, strings.Count(string(raw), "Recorded observation: amber."), "recent evidence and retained evidence are not duplicated")
	for _, req := range m.requests {
		encoded, _ := json.Marshal(req)
		require.NotContains(t, string(encoded), "UNAPPROVED SECRET")
	}
	var extraction, text string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT extraction_status,extracted_text FROM attachments WHERE id=$1", denied.ID).Scan(&extraction, &text))
	require.Equal(t, "pending", extraction)
	require.Empty(t, text)
	page, e := f.repo.ListMessages(f.ctx, c.ID, "", 100)
	require.NoError(t, e)
	coverage := page.Messages[len(page.Messages)-1].Context.Coverage.Evidence
	require.NotNil(t, coverage)
	require.Len(t, coverage.Included, 1)
	require.Equal(t, approvedScope, coverage.Included[0].Scope)
	require.Zero(t, coverage.Omitted)
	quotes := page.Messages[len(page.Messages)-1].Context.Coverage.Quotations
	require.NotNil(t, quotes)
	require.Equal(t, 3, quotes.Eligible)
	require.Len(t, quotes.Included, 3)
	require.Equal(t, originalUserID, quotes.Included[0].User.SourceID)
	require.Contains(t, string(raw), originalUserID, "original user source survives worker/store restart separately from generated notes")
	require.Contains(t, string(raw), "assistantGeneratedNotes")
	require.Contains(t, string(raw), "assistantReplies")
	require.Contains(t, string(raw), "userQuotations")
}
