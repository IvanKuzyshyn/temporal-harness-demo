//go:build integration

package webintegration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/stretchr/testify/require"
)

type heldDocumentModel struct {
	started, release chan struct{}
	once             sync.Once
}

func (m *heldDocumentModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	if len(r.Tools) == 0 {
		m.once.Do(func() { close(m.started) })
		select {
		case <-m.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "authorized summary"}}, nil
}
func TestAnalysisChildScopeCannotExpand(t *testing.T) {
	f := setup(t)
	s, e := attachments.NewStore(t.TempDir(), f.repo, nil)
	require.NoError(t, e)
	defer s.Close()
	c := f.conversation(t, "qwen3.5:9b")
	file, e := s.Put(f.ctx, c.ID, strings.NewReader("approved content"), attachments.UploadMetadata{Name: "same.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	other, e := s.Put(f.ctx, c.ID, strings.NewReader("UNAPPROVED SECRET"), attachments.UploadMetadata{Name: "same.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	m := &heldDocumentModel{started: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(m.release) })
	a := &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: s}
	runner, e := worker.StartWeb(f.ctx, f.address, a)
	require.NoError(t, e)
	defer runner.Close()
	defer release()
	cmd := f.attachmentCommand(t, c, file.ID)
	_, e = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, e)
	pending := f.pending(t, c)
	in := chat.DocumentAnalysisInput{Scope: pending.Scope, Model: c.Model, Budget: chat.DefaultDocumentBudget()}
	_, e = a.PrepareDocumentAnalysis(f.ctx, in)
	require.Error(t, e, "pending scope is not approval")
	_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalOnce))
	require.NoError(t, e)
	waitBarrier(t, m.started)
	// Original and pure-extraction cache are both still behind the same scope.
	_, e = s.Extract(f.ctx, pending.Scope, other.ID)
	require.Error(t, e)
	for _, mutate := range []func(*chat.DocumentAnalysisInput){
		func(v *chat.DocumentAnalysisInput) { v.Model = "gemma4:e4b" },
		func(v *chat.DocumentAnalysisInput) { v.Budget.MaxChunks = 33 },
		func(v *chat.DocumentAnalysisInput) { v.Scope.ContentRef = other.Checksum },
		func(v *chat.DocumentAnalysisInput) { v.Scope.ActionHash = strings.Repeat("0", 64) },
		func(v *chat.DocumentAnalysisInput) { v.Scope.PolicyRevision++ },
		func(v *chat.DocumentAnalysisInput) { v.Scope.ToolName = "web_fetch" },
	} {
		bad := in
		mutate(&bad)
		_, e = a.PrepareDocumentAnalysis(f.ctx, bad)
		require.Error(t, e)
	}
	// Display events and filename are not authority or analysis cache identity.
	_, e = f.pool.Exec(f.ctx, "DELETE FROM events WHERE conversation_id=$1", c.ID)
	require.NoError(t, e)
	_, e = f.pool.Exec(f.ctx, "UPDATE attachments SET original_name='renamed.txt' WHERE id=$1", file.ID)
	require.NoError(t, e)
	_, e = a.PrepareDocumentAnalysis(f.ctx, in)
	require.NoError(t, e, "same immutable manifest can be read during retry")
	var text string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT extracted_text FROM attachments WHERE id=$1", other.ID).Scan(&text))
	require.Empty(t, text)
	release()
	f.completed(t, cmd)
}

func analysisPDF(text string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	var offsets []int
	for i, o := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 6\n0000000000 65535 f \n")
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Root 1 0 R /Size 6 >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
func analysisDOCX(t *testing.T) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, v := range [][2]string{{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`}, {"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>actual DOCX child extraction</w:t></w:r></w:p></w:body></w:document>`}} {
		w, e := z.Create(v[0])
		require.NoError(t, e)
		_, e = w.Write([]byte(v[1]))
		require.NoError(t, e)
	}
	require.NoError(t, z.Close())
	return b.Bytes()
}
func TestDocumentPDFDOCXChildHelperSQL(t *testing.T) {
	for _, v := range []struct {
		name, media, want string
		data              []byte
	}{{"real.pdf", "application/pdf", "actual PDF child extraction", analysisPDF("actual PDF child extraction")}, {"real.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "actual DOCX child extraction", analysisDOCX(t)}} {
		t.Run(v.name, func(t *testing.T) {
			f := setup(t)
			s, e := attachments.NewStore(t.TempDir(), f.repo, nil)
			require.NoError(t, e)
			defer s.Close()
			c := f.conversation(t, "gemma4:e4b")
			file, e := s.Put(f.ctx, c.ID, bytes.NewReader(v.data), attachments.UploadMetadata{Name: v.name, MediaType: v.media})
			require.NoError(t, e)
			m := &documentModel{}
			runner, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: s})
			require.NoError(t, e)
			defer runner.Close()
			cmd := f.attachmentCommand(t, c, file.ID)
			_, e = f.gateway.Submit(f.ctx, cmd)
			require.NoError(t, e)
			p := f.pending(t, c)
			_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
			require.NoError(t, e)
			f.completed(t, cmd)
			var text string
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT extracted_text FROM attachments WHERE id=$1", file.ID).Scan(&text))
			require.Contains(t, text, v.want)
			result, e := f.repo.WebToolResult(f.ctx, c.ID, p.Scope.ToolCallID)
			require.NoError(t, e)
			require.NotNil(t, result.Analysis)
			require.Equal(t, 1, result.Analysis.Chunks)
			require.Len(t, result.Coverage, 1)
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			require.NoError(t, f.sdk.GetWorkflow(ctx, result.Analysis.ChildID, "").Get(ctx, nil))
			t.Log("actual child→pinned sandbox parser→SQL extraction and summary; only model transport injected")
		})
	}
}
