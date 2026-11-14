//go:build integration

package webintegration_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestDocumentBudgetDurablePartialCoverage(t *testing.T) {
	f := setup(t)
	store, e := attachments.NewStore(t.TempDir(), f.repo, nil)
	require.NoError(t, e)
	defer store.Close()
	c := f.conversation(t, "qwen3.5:9b")
	file, e := store.Put(f.ctx, c.ID, strings.NewReader(strings.Repeat("x", 128001)), attachments.UploadMetadata{Name: "budget.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	model := &documentModel{}
	runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: model, Attachments: store})
	require.NoError(t, e)
	defer runner.Close()
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	p := f.pending(t, c)
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
	require.NoError(t, e)
	f.completed(t, cmd)
	result, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
	require.NoError(t, e)
	require.True(t, result.Truncated)
	require.Equal(t, "partial", result.ExtractionStatus)
	require.NotNil(t, result.Analysis)
	require.True(t, result.Analysis.Truncated)
	require.Equal(t, 32, result.Analysis.Chunks)
	require.Len(t, result.Coverage, 32)
	require.Equal(t, 128000, result.Coverage[31].EndByte)
	var parts int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM document_analysis_parts WHERE tool_call_id=$1", p.Scope.ToolCallID).Scan(&parts))
	require.Equal(t, 33, parts)
	model.mu.Lock()
	defer model.mu.Unlock()
	require.Equal(t, 32, model.chunks)
}
