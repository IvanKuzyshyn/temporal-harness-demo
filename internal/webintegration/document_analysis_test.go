//go:build integration

package webintegration_test

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
)

type reverseDocumentRepository struct {
	*storage.Repository
	ready   chan int
	release [4]chan struct{}
}

func (r *reverseDocumentRepository) SaveDocumentPart(ctx context.Context, in chat.DocumentAnalysisInput, attachment chat.AttachmentID, index int, owner string, out chat.DocumentPart) error {
	if index >= 0 && index < 4 {
		select {
		case r.ready <- index:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-r.release[index]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.Repository.SaveDocumentPart(ctx, in, attachment, index, owner, out)
}

type documentModel struct {
	mu                   sync.Mutex
	active, peak, chunks int
}

func (m *documentModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	if len(r.Tools) > 0 {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Analysis recorded"}}, nil
	}
	m.mu.Lock()
	m.active++
	m.chunks++
	if m.active > m.peak {
		m.peak = m.active
	}
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.active--; m.mu.Unlock() }()
	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "bounded chunk summary"}}, nil
}
func TestDocumentChunksParallelAndOrdered(t *testing.T) {
	f := setup(t)
	store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	c := f.conversation(t, "qwen3.5:9b")
	file, e := store.Put(f.ctx, c.ID, strings.NewReader(strings.Repeat("document section. ", 2000)), attachments.UploadMetadata{Name: "long.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	m := &documentModel{}
	repo := &reverseDocumentRepository{Repository: f.repo, ready: make(chan int, 4)}
	for i := range repo.release {
		repo.release[i] = make(chan struct{})
	}
	var releases [4]func()
	for i := range releases {
		releases[i] = sync.OnceFunc(func() { close(repo.release[i]) })
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: repo, Model: m, Attachments: store})
	require.NoError(t, e)
	defer runner.Close()
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	pending := f.pending(t, c)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalOnce))
	require.NoError(t, e)
	for range 4 {
		select {
		case <-repo.ready:
		case <-time.After(10 * time.Second):
			t.Fatal("four chunk activity bodies did not overlap")
		}
	}
	for i := 3; i >= 0; i-- {
		releases[i]()
		require.Eventually(t, func() bool {
			var count int
			err := f.pool.QueryRow(f.ctx, "SELECT count(*) FROM document_analysis_parts WHERE tool_call_id=$1 AND part_index=$2", pending.Scope.ToolCallID, i).Scan(&count)
			return err == nil && count == 1
		}, 5*time.Second, 10*time.Millisecond)
	}
	f.completed(t, cmd)
	children := 0
	it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		event, err := it.Next()
		require.NoError(t, err)
		if a := event.GetChildWorkflowExecutionStartedEventAttributes(); a != nil {
			require.Equal(t, "DocumentAnalysisWorkflow", a.WorkflowType.Name)
			children++
		}
	}
	require.Equal(t, 1, children, "approved document analysis must execute a real child")
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Greater(t, m.chunks, 4)
	require.LessOrEqual(t, m.peak, 4)
	result, e := f.repo.WebToolResult(f.ctx, c.ID, pending.Scope.ToolCallID)
	require.NoError(t, e)
	require.Contains(t, result.Text, "bounded chunk summary")
	require.NotNil(t, result.Analysis)
	require.Equal(t, 9, result.Analysis.Chunks)
	require.Len(t, result.Coverage, 9)
	for i, coverage := range result.Coverage {
		require.Equal(t, i, coverage.Index)
		require.Equal(t, i*4000, coverage.StartByte)
	}
	require.Equal(t, 36000, result.Coverage[8].EndByte)
	require.False(t, result.Truncated)
	oversized := result
	oversized.Coverage = make([]chat.DocumentCoverage, 33)
	require.ErrorIs(t, f.repo.SaveWebToolResult(f.ctx, pending.Scope, uuid.NewString(), oversized), chat.ErrInvalid, "metadata bounds enforced even on canonical retry")
	t.Log("four real chunk bodies overlapped; first four SQL summaries committed 3,2,1,0; final coverage ordered 0..8; default model generation peak", m.peak)
}
