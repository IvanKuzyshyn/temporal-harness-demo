package attachments

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageValidationBounds(t *testing.T) {
	s, _, c := fixtureStore(t)
	for _, size := range []image.Point{{2, 2}, {8193, 1}, {5000, 4001}} {
		var b bytes.Buffer
		require.NoError(t, png.Encode(&b, image.NewGray(image.Rectangle{Max: size})))
		_, e := s.Put(context.Background(), c, &b, UploadMetadata{Name: "a.png", MediaType: "image/png"})
		if size.X == 2 {
			require.NoError(t, e)
		} else {
			require.Error(t, e)
		}
	}
}
func docx(t *testing.T, xml string, extras map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	extras2 := map[string]string{"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`, "word/document.xml": xml}
	for k, v := range extras {
		extras2[k] = v
	}
	for k, v := range extras2 {
		f, e := w.Create(k)
		require.NoError(t, e)
		_, e = f.Write([]byte(v))
		require.NoError(t, e)
	}
	require.NoError(t, w.Close())
	return b.Bytes()
}
func pdfFixture(text string) []byte {
	stream := ""
	if text != "" {
		stream = "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offsets := []int{}
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
func realParser(t *testing.T) ContainerParser {
	t.Helper()
	if os.Getenv("WEBCHAT_TEST_PARSERS") != "1" {
		t.Skip("explicit isolated parser container gate")
	}
	p, err := NewContainerParser(os.Getenv("WEBCHAT_PARSER_IMAGE_ID"))
	require.NoError(t, err)
	return p
}
func TestParsersValidateWithoutSemanticExtraction(t *testing.T) {
	p := realParser(t)
	for _, v := range []struct {
		mime string
		b    []byte
	}{{"application/pdf", pdfFixture("Secret hello document")}, {docxType, docx(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Secret document</w:t></w:r></w:p></w:body></w:document>`, nil)}, {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect width="20" height="20" fill="red"/></svg>`)}} {
		t.Log(v.mime)
		d, e := p.Parse(context.Background(), v.b, v.mime, false)
		require.NoError(t, e)
		require.Empty(t, d.Text)
		d, e = p.Parse(context.Background(), v.b, v.mime, true)
		require.NoError(t, e)
		if v.mime != "image/svg+xml" {
			require.Contains(t, d.Text, "Secret")
		}
	}
}
func TestDocxZipBomb(t *testing.T) {
	p := realParser(t)
	b := docx(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`, map[string]string{"word/bomb.bin": strings.Repeat("a", 16*1024*1024+1)})
	_, e := p.Parse(context.Background(), b, docxType, false)
	require.Error(t, e)
}
func TestScannedPDFUnsupported(t *testing.T) {
	p := realParser(t)
	d, e := p.Parse(context.Background(), scannedPDF(t), "application/pdf", true)
	require.NoError(t, e)
	require.Equal(t, "unsupported", d.Status)
	require.Equal(t, "image_only_or_empty_pdf", d.Reason)
}
func TestMaliciousSVGAndDOCX(t *testing.T) {
	p := realParser(t)
	for _, xml := range []string{`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><script>alert(1)</script></svg>`, `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg">&x;</svg>`, `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><image href="https://example.com/a.png"/></svg>`, `<svg xmlns="http://www.w3.org/2000/svg" width="99999" height="20"/>`, `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect style="fill:url(file:///etc/passwd)"/></svg>`} {
		_, e := p.Parse(context.Background(), []byte(xml), "image/svg+xml", false)
		require.Error(t, e)
	}
	_, e := p.Parse(context.Background(), docx(t, `<!DOCTYPE x [<!ENTITY a SYSTEM "https://example.com/">]><x>&a;</x>`, nil), docxType, true)
	require.Error(t, e)
}
