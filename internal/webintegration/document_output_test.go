//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Only inference is injected. Authorization, activities, Temporal and SQL are real.
// Hold the parent after tool publication so canonical activity retries still have
// an active turn and can exercise the actual ownership/ACK checks.
type outputStateModel struct {
	response llm.Response
	parent   chan llm.Request
	release  chan struct{}
	calls    atomic.Int32
}

func (m *outputStateModel) Chat(ctx context.Context, req llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	if len(req.Tools) > 0 {
		if m.parent != nil {
			select {
			case m.parent <- req:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
			select {
			case <-m.release:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Analysis recorded"}}, nil
	}
	m.calls.Add(1)
	return m.response, nil
}

func TestDocumentOutputStateAndCanonicalRetry(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		tokens     int
		partial    bool
	}{
		{"usable", "usable summary", 12, false},
		{"exhausted_short", "unfinished summary", 2000, true},
		{"over_cap_short", "unfinished summary", 2001, true},
		{"byte_clipped", strings.Repeat("s", 1001), 12, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
			require.NoError(t, e)
			defer store.Close()
			c := f.conversation(t, "qwen3.5:9b")
			file, e := store.Put(f.ctx, c.ID, strings.NewReader("source"), attachments.UploadMetadata{Name: "source.txt", MediaType: "text/plain"})
			require.NoError(t, e)
			m := &outputStateModel{response: llm.Response{Message: llm.Message{Role: "assistant", Content: tc.text}, Tokens: tc.tokens}, parent: make(chan llm.Request, 1), release: make(chan struct{})}
			a := &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store}
			runner, e := worker.StartWeb(f.ctx, f.address, a)
			require.NoError(t, e)
			defer runner.Close()
			release := sync.OnceFunc(func() { close(m.release) })
			defer release()
			cmd := f.attachmentCommand(t, c, file.ID)
			_, e = f.gateway.Submit(f.ctx, cmd)
			require.NoError(t, e)
			p := f.pending(t, c)
			_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
			require.NoError(t, e)
			var parent llm.Request
			select {
			case parent = <-m.parent:
			case <-time.After(10 * time.Second):
				t.Fatal("parent did not consume durable tool result")
			}
			in := chat.DocumentAnalysisInput{Scope: p.Scope, Model: c.Model, Budget: chat.DefaultDocumentBudget()}
			owner := uuid.NewString()
			_, _, e = f.repo.BeginWebTool(f.ctx, in.Scope, owner, 1)
			require.NoError(t, e)
			defer func() { require.NoError(t, f.repo.AcknowledgeWebToolCleanup(f.ctx, in.Scope, owner)) }()
			part, e := f.repo.ReadDocumentPart(f.ctx, in, file.ID, 0)
			require.NoError(t, e)
			require.Equal(t, tc.partial, part.Coverage.SummaryTruncated, "output exhaustion must survive durable chunk publication")
			require.Equal(t, 6, part.Coverage.EndByte, "source bytes do not imply a complete summary")
			require.NotEmpty(t, strings.TrimSpace(part.Text))
			result, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
			require.NoError(t, e)
			want := "completed"
			if tc.partial {
				want = "partial"
			}
			require.Equal(t, want, result.Analysis.Status)
			require.Equal(t, tc.partial, result.Truncated)
			require.Equal(t, tc.partial, result.Analysis.Truncated)
			require.Len(t, result.Coverage, 1)
			require.Equal(t, tc.partial, result.Coverage[0].SummaryTruncated)
			parentSaw := false
			for _, msg := range parent.Messages {
				if msg.Role == "tool" {
					var observed chat.WebToolResult
					require.NoError(t, json.Unmarshal([]byte(msg.Content), &observed))
					require.Equal(t, want, observed.Analysis.Status)
					require.Equal(t, tc.partial, observed.Truncated)
					parentSaw = true
				}
			}
			require.True(t, parentSaw, "parent model sees partial metadata, not just text")
			require.NoError(t, a.AnalyzeDocumentChunk(f.ctx, chat.DocumentChunkInput{Analysis: in, Index: 0}))
			require.EqualValues(t, 1, m.calls.Load(), "canonical retry does not regenerate or erase exhaustion")
			for _, text := range []string{"", " \t\n\u2003"} {
				bad := part
				bad.Text = text
				require.ErrorIs(t, f.repo.SaveDocumentPart(f.ctx, in, file.ID, 0, owner, bad), chat.ErrInvalid, "storage must reject unusable summaries even when a winner already exists")
			}
			alternate := part
			alternate.Text = "different retry output"
			alternate.Coverage.SummaryTruncated = false
			require.NoError(t, f.repo.SaveDocumentPart(f.ctx, in, file.ID, 0, owner, alternate))
			canonical, e := f.repo.ReadDocumentPart(f.ctx, in, file.ID, 0)
			require.NoError(t, e)
			require.Equal(t, part, canonical)
			finalized, e := a.FinishDocumentAnalysis(f.ctx, in)
			require.NoError(t, e)
			require.Equal(t, *result.Analysis, finalized)
			again, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
			require.NoError(t, e)
			require.Equal(t, result, again)
			require.NoError(t, f.repo.AcknowledgeWebToolCleanup(f.ctx, in.Scope, owner))
			release()
			f.completed(t, cmd)
			assertOutputCleanup(t, f, cmd.TurnID)
		})
	}
}

func assertOutputCleanup(t *testing.T, f *fixture, turn chat.TurnID) {
	t.Helper()
	var missing int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL", turn).Scan(&missing))
	require.Zero(t, missing, "every started physical body, including rejected output, must acknowledge actual return")
}

func TestDocumentAndImageUnusableOutputFails(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		tokens     int
		image      bool
	}{
		{"document_empty", "", 0, false},
		{"document_exhausted_empty", "", 2000, false},
		{"document_whitespace", " \t\r\n\u2003", 2000, false},
		{"document_clipped_to_whitespace", strings.Repeat(" ", 1000) + "tail", 12, false},
		{"image_whitespace", " \t\r\n\u2003", 12, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
			require.NoError(t, e)
			defer store.Close()
			c := f.conversation(t, "qwen3.5:9b")
			data := []byte("source")
			meta := attachments.UploadMetadata{Name: "source.txt", MediaType: "text/plain"}
			if tc.image {
				data, e = os.ReadFile("../../scripts/testdata/bonjour.png")
				require.NoError(t, e)
				meta = attachments.UploadMetadata{Name: "source.png", MediaType: "image/png"}
			}
			file, e := store.Put(f.ctx, c.ID, bytes.NewReader(data), meta)
			require.NoError(t, e)
			m := &outputStateModel{response: llm.Response{Message: llm.Message{Role: "assistant", Content: tc.text}, Tokens: tc.tokens}}
			runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store})
			require.NoError(t, e)
			defer runner.Close()
			cmd := f.attachmentCommand(t, c, file.ID)
			_, e = f.gateway.Submit(f.ctx, cmd)
			require.NoError(t, e)
			p := f.pending(t, c)
			_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
			require.NoError(t, e)
			var turn chat.Turn
			require.Eventually(t, func() bool {
				turn, e = f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
				return e == nil && (turn.Status == "failed" || turn.Status == "completed")
			}, 10*time.Second, 20*time.Millisecond)
			require.Equal(t, "failed", turn.Status, "unusable model output must not become parent-visible completed analysis")
			failed, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
			require.NoError(t, e)
			require.Equal(t, "failed", failed.Error, "parent settlement persists failure, not an analysis result")
			require.Nil(t, failed.Analysis)
			require.Empty(t, failed.Text)
			require.Empty(t, failed.Coverage)
			var chunks int
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM document_analysis_parts WHERE tool_call_id=$1 AND part_index>=0", p.Scope.ToolCallID).Scan(&chunks))
			require.Zero(t, chunks, "no usable-summary coverage was produced")
			assertOutputCleanup(t, f, cmd.TurnID)
		})
	}
}

// Simulate a pre-fix persisted invalid part without weakening production writes.
// Pause the original activity after insertion, before finalization, to exercise
// both direct retry and finalization against that same legacy cache row.
type invalidCachedPartRepository struct {
	*storage.Repository
	afterSave func(context.Context, chat.DocumentAnalysisInput) error
}

func (r *invalidCachedPartRepository) SaveDocumentPart(ctx context.Context, in chat.DocumentAnalysisInput, id chat.AttachmentID, index int, owner string, part chat.DocumentPart) error {
	if e := r.Repository.SaveDocumentPart(ctx, in, id, index, owner, part); e != nil {
		return e
	}
	if index >= 0 {
		return r.afterSave(ctx, in)
	}
	return nil
}
func TestDocumentInvalidCachedSummaryCannotComplete(t *testing.T) {
	f := setup(t)
	store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	c := f.conversation(t, "qwen3.5:9b")
	file, e := store.Put(f.ctx, c.ID, strings.NewReader("source"), attachments.UploadMetadata{Name: "source.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	ready, unblock := make(chan struct{}), make(chan struct{})
	repo := &invalidCachedPartRepository{Repository: f.repo, afterSave: func(ctx context.Context, in chat.DocumentAnalysisInput) error {
		_, e := f.pool.Exec(ctx, `UPDATE document_analysis_parts SET result=jsonb_set(result,'{Text}','"   "'::jsonb) WHERE tool_call_id=$1 AND part_index=0`, in.Scope.ToolCallID)
		if e != nil {
			return e
		}
		close(ready)
		select {
		case <-unblock:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	m := &outputStateModel{response: llm.Response{Message: llm.Message{Role: "assistant", Content: "valid original summary"}}}
	runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: repo, Model: m, Attachments: store})
	require.NoError(t, e)
	defer runner.Close()
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	p := f.pending(t, c)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
	require.NoError(t, e)
	waitBarrier(t, ready)
	in := chat.DocumentAnalysisInput{Scope: p.Scope, Model: c.Model, Budget: chat.DefaultDocumentBudget()}
	a := &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store}
	require.Error(t, a.AnalyzeDocumentChunk(f.ctx, chat.DocumentChunkInput{Analysis: in, Index: 0}), "retry must not adopt unusable cached text as successful analysis")
	_, e = a.FinishDocumentAnalysis(f.ctx, in)
	require.Error(t, e, "finalization must reject invalid cached summaries")
	_, e = f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
	require.ErrorIs(t, e, storage.ErrNotFound)
	require.EqualValues(t, 1, m.calls.Load(), "invalid immutable cache is not silently replaced")
	release()
	require.Eventually(t, func() bool {
		turn, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
		return e == nil && turn.Status == "failed"
	}, 10*time.Second, 20*time.Millisecond)
	assertOutputCleanup(t, f, cmd.TurnID)
}
