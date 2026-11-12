//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
)

type imageReadProbe struct {
	*storage.Repository
	reads atomic.Int32
}

func (r *imageReadProbe) GetAttachment(ctx context.Context, c chat.ConversationID, id chat.AttachmentID) (chat.Attachment, error) {
	r.reads.Add(1)
	return r.Repository.GetAttachment(ctx, c, id)
}
func TestImageScopeFailureHasNoContentOrModelIO(t *testing.T) {
	f := setup(t)
	r := &imageReadProbe{Repository: f.repo}
	s, e := attachments.NewStore(t.TempDir(), r, nil)
	require.NoError(t, e)
	defer s.Close()
	c := f.conversation(t, "qwen3.5:9b")
	pngData, e := os.ReadFile("../../scripts/testdata/bonjour.png")
	require.NoError(t, e)
	file, e := s.Put(f.ctx, c.ID, bytes.NewReader(pngData), attachments.UploadMetadata{Name: "same.png", MediaType: "image/png"})
	require.NoError(t, e)
	other, e := s.Put(f.ctx, c.ID, bytes.NewReader(pngData), attachments.UploadMetadata{Name: "same.png", MediaType: "image/png"})
	require.NoError(t, e)
	held := &heldDocumentModel{started: make(chan struct{}), release: make(chan struct{})}
	m := &tracingImageModel{base: held}
	a := &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: s}
	runner, e := worker.StartWeb(f.ctx, f.address, a)
	require.NoError(t, e)
	defer runner.Close()
	defer close(held.release)
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	p := f.pending(t, c)
	in := chat.DocumentAnalysisInput{Scope: p.Scope, Model: c.Model, Budget: chat.DefaultDocumentBudget()}
	before := r.reads.Load()
	_, e = a.AnalyzeAttachmentImage(f.ctx, in)
	require.Error(t, e)
	require.Equal(t, before, r.reads.Load())
	require.Zero(t, m.calls.Load())
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
	require.NoError(t, e)
	waitBarrier(t, held.started)
	before = r.reads.Load()
	for _, mutate := range []func(*chat.DocumentAnalysisInput){
		func(v *chat.DocumentAnalysisInput) { v.Model = "gemma4:e4b" }, func(v *chat.DocumentAnalysisInput) { v.Scope.ActionHash = strings.Repeat("0", 64) }, func(v *chat.DocumentAnalysisInput) { v.Scope.ContentRef = strings.Repeat("b", 64) }, func(v *chat.DocumentAnalysisInput) { v.Scope.PolicyRevision++ }, func(v *chat.DocumentAnalysisInput) { v.Scope.ToolName = "web_fetch" }, func(v *chat.DocumentAnalysisInput) { v.Budget.Parallelism = 5 },
	} {
		bad := in
		mutate(&bad)
		_, e = a.AnalyzeAttachmentImage(f.ctx, bad)
		require.Error(t, e)
		require.Equal(t, before, r.reads.Load())
		require.EqualValues(t, 1, m.calls.Load())
	}
	_, e = s.ResolveImage(f.ctx, p.Scope, llm.ImageReference{AttachmentID: other.ID, DerivedKey: llm.NarrowImageProfile, Checksum: file.Checksum})
	require.Error(t, e)
	require.Equal(t, before, r.reads.Load())
	_, e = f.pool.Exec(f.ctx, "DELETE FROM events WHERE conversation_id=$1", c.ID)
	require.NoError(t, e)
	_, e = f.pool.Exec(f.ctx, "UPDATE attachments SET original_name='renamed.png' WHERE id=$1", file.ID)
	require.NoError(t, e)
	_, e = s.ResolveImage(f.ctx, p.Scope, llm.ImageReference{AttachmentID: file.ID, DerivedKey: llm.NarrowImageProfile, Checksum: file.Checksum})
	require.NoError(t, e, "display/name changes do not change immutable authorization")
}
