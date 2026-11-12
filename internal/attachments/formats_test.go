package attachments

import (
	"bytes"
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
)

func TestAllRequiredFormatsStoreOriginalBytes(t *testing.T) {
	realParser(t)
	s, _, c := fixtureStore(t)
	var pngData, jpegData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 10, 10))))
	require.NoError(t, jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 10, 10)), nil))
	for _, f := range []struct {
		name, media string
		data        []byte
	}{{"a.txt", "text/plain", []byte("original text")}, {"a.md", "text/markdown", []byte("# original markdown")}, {"a.pdf", "application/pdf", pdfFixture("original PDF")}, {"a.docx", docxType, docx(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>original DOCX</w:t></w:r></w:p></w:body></w:document>`, nil)}, {"a.png", "image/png", pngData.Bytes()}, {"a.jpg", "image/jpeg", jpegData.Bytes()}, {"a.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><circle cx="5" cy="5" r="4"/></svg>`)}} {
		t.Run(f.name, func(t *testing.T) {
			a, e := s.Put(context.Background(), c, bytes.NewReader(f.data), UploadMetadata{Name: f.name, MediaType: f.media})
			require.NoError(t, e)
			require.Empty(t, a.ExtractedText)
			require.Equal(t, "ready", a.Status)
			reader, e := s.Open(context.Background(), c, a.ID)
			require.NoError(t, e)
			got, e := io.ReadAll(reader)
			reader.Close()
			require.NoError(t, e)
			require.Equal(t, f.data, got)
		})
	}
}
func scannedPDF(t *testing.T) []byte {
	var b bytes.Buffer
	require.NoError(t, jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 10, 10)), nil))
	stream := "q 100 0 0 100 72 600 cm /Im0 Do Q"
	return pdfObjects([]string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>", fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 10 /Height 10 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n%s\nendstream", b.Len(), b.String()), fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}, "")
}

func TestEncryptedPDFUnsupported(t *testing.T) {
	p := realParser(t)
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>", "<< /Filter /Standard /V 1 /R 2 /O <0000000000000000000000000000000000000000000000000000000000000000> /U <0000000000000000000000000000000000000000000000000000000000000000> /P -4 >>"}
	d, e := p.Parse(context.Background(), pdfObjects(objects, "/Encrypt 4 0 R /ID [<01020304><01020304>]"), "application/pdf", true)
	require.NoError(t, e)
	require.Equal(t, "unsupported", d.Status)
	require.Equal(t, "encrypted_pdf", d.Reason)
}
