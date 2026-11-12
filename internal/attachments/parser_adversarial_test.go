package attachments

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pdfObjects(objects []string, trailer string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := []int{}
	for i, o := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Root 1 0 R /Size %d %s >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, trailer, xref)
	return b.Bytes()
}
func manyPagePDF(n int) []byte {
	kids := []string{}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	for i := 0; i < n; i++ {
		page := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
		text := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (page %d) Tj ET", i+1)
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", page+1), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(text), text))
	}
	objects[1] = "<< /Type /Pages /Kids [" + strings.Join(kids, " ") + fmt.Sprintf("] /Count %d >>", n)
	return pdfObjects(objects, "")
}
func TestPDFPageCapAndLocalStandardFonts(t *testing.T) {
	p := realParser(t)
	d, e := p.Parse(context.Background(), manyPagePDF(101), "application/pdf", true)
	require.NoError(t, e)
	require.Equal(t, 101, d.Pages)
	require.Len(t, d.Sections, 100)
	require.True(t, d.Truncated)
	require.Contains(t, d.Text, "page 100")
	require.NotContains(t, d.Text, "page 101")
}
func TestPDFExpandedStreamCap(t *testing.T) {
	p := realParser(t)
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, e := w.Write([]byte(strings.Repeat(" ", 16777217) + "BT /F1 12 Tf (end) Tj ET"))
	require.NoError(t, e)
	require.NoError(t, w.Close())
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", b.Len(), b.String())}
	_, e = p.Parse(context.Background(), pdfObjects(objects, ""), "application/pdf", true)
	require.Error(t, e)
	require.Contains(t, e.Error(), "expanded PDF limit")
}
func TestDocxExternalRelationshipsIgnoredAndTextCapped(t *testing.T) {
	p := realParser(t)
	b := docx(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("é", 200001)+`</w:t></w:r></w:p></w:body></w:document>`, map[string]string{"word/_rels/document.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="evil" Target="http://127.0.0.1:9/secret" TargetMode="External" Type="image"/></Relationships>`, "word/vbaProject.bin": "never execute macro"})
	d, e := p.Parse(context.Background(), b, docxType, true)
	require.NoError(t, e)
	require.Equal(t, "completed", d.Status)
	require.Len(t, []rune(d.Text), 200000)
	require.True(t, d.Truncated)
}
func TestSVGTallImageAndSingleQuotedDimensions(t *testing.T) {
	p := realParser(t)
	_, e := p.Parse(context.Background(), []byte(`<svg xmlns='http://www.w3.org/2000/svg' width='20' height='8192'><rect width='20' height='8192'/></svg>`), "image/svg+xml", false)
	require.NoError(t, e)
}
func TestParserCancellationRemovesOwnedContainer(t *testing.T) {
	p := realParser(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, e := p.Parse(ctx, manyPagePDF(100), "application/pdf", true)
	require.Error(t, e)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	out, e := exec.Command("docker", "ps", "-aq", "--filter", "name=webchat-parser-").Output()
	require.NoError(t, e)
	require.Empty(t, strings.TrimSpace(string(out)), "no detached parser container after cancellation")
}

// Exercise the actual image with exactly the production restrictions and retain
// stderr: the standard-font warning must be resolved, not just verbosity-muted.
func TestPDFStandardFontWarningAbsent(t *testing.T) {
	realParser(t)
	req, _ := json.Marshal(map[string]any{"media": "application/pdf", "data": base64.StdEncoding.EncodeToString(pdfFixture("Hello standard fonts")), "extract": true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--memory", "512m", "--memory-swap", "512m", "--pids-limit", "32", "--cpus", "1", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-i", ParserImage)
	cmd.Stdin = bytes.NewReader(req)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	require.Empty(t, stderr.String())
	require.NotContains(t, stdout.String(), "Warning")
	var d Document
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &d))
	require.Contains(t, d.Text, "Hello standard fonts")
}
