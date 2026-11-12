package attachments

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func (s *Store) validate(ctx context.Context, b []byte, m UploadMetadata) (string, error) {
	media, _, e := mime.ParseMediaType(m.MediaType)
	if e != nil {
		return "", ErrInvalid
	}
	ext := strings.ToLower(filepath.Ext(m.Name))
	expected := map[string]string{".txt": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown", ".pdf": "application/pdf", ".docx": docxType, ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".svg": "image/svg+xml"}[ext]
	if media != expected || expected == "" {
		return "", fmt.Errorf("signature/type/extension mismatch: %w", ErrInvalid)
	}
	switch media {
	case "text/plain", "text/markdown":
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return "", ErrInvalid
		}
	case "image/png", "image/jpeg":
		cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
		if e != nil || "image/"+format != media || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 20000000 {
			return "", fmt.Errorf("raster must decode and fit 8192 dimensions/20MP: %w", ErrInvalid)
		}
		if _, _, e = image.Decode(bytes.NewReader(b)); e != nil {
			return "", fmt.Errorf("unreadable raster: %w", ErrInvalid)
		}
	case "application/pdf", docxType, "image/svg+xml":
		if media == "application/pdf" && !bytes.HasPrefix(b, []byte("%PDF-")) {
			return "", ErrInvalid
		}
		if media == docxType && !bytes.HasPrefix(b, []byte("PK\x03\x04")) {
			return "", ErrInvalid
		}
		if _, e = s.parser.Parse(ctx, b, media, false); e != nil {
			return "", e
		}
	default:
		return "", ErrInvalid
	}
	return media, ctx.Err()
}
