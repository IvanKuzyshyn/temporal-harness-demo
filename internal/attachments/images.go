package attachments

import (
	"bytes"
	"context"
	"image"
	"image/png"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

type ImageReference = llm.ImageReference
type ResolvedImage struct {
	Reference     ImageReference
	Data          []byte
	Width, Height int
}

// ResolveImage is an activity-only operation. Upload/download permissions are
// not model permissions. Authorize before cache, metadata or original reads.
func (s *Store) ResolveImage(ctx context.Context, scope chat.ApprovalScope, ref ImageReference) (ResolvedImage, error) {
	var out ResolvedImage
	r, ok := s.repo.(interface {
		AuthorizeAttachment(context.Context, chat.ApprovalScope, chat.AttachmentID) error
	})
	if !ok {
		return out, ErrInvalid
	}
	if err := r.AuthorizeAttachment(ctx, scope, ref.AttachmentID); err != nil {
		return out, err
	}
	if ref.DerivedKey != "model-png-v1" || ref.Checksum != scope.ContentRef {
		return out, ErrInvalid
	}
	a, err := s.repo.GetAttachment(ctx, scope.ConversationID, ref.AttachmentID)
	if err != nil {
		return out, err
	}
	if a.Checksum != ref.Checksum || a.Status != "ready" {
		return out, ErrInvalid
	}
	b, err := s.readOriginal(a)
	if err != nil {
		return out, err
	}
	switch a.MediaType {
	case "image/svg+xml":
		p, ok := s.parser.(interface {
			Rasterize(context.Context, []byte) ([]byte, error)
		})
		if !ok {
			return out, ErrInvalid
		}
		b, err = p.Rasterize(ctx, b)
		if err != nil {
			return out, err
		}
	case "image/png", "image/jpeg":
	default:
		return out, ErrInvalid
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	b, w, h, err := prepareRaster(b)
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return ResolvedImage{Reference: ref, Data: b, Width: w, Height: h}, nil
}
func prepareRaster(data []byte) ([]byte, int, int, error) {
	if len(data) > MaxBytes {
		return nil, 0, 0, ErrTooLarge
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 20000000 {
		return nil, 0, 0, ErrInvalid
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, ErrInvalid
	}
	w, h := cfg.Width, cfg.Height
	if w > 2048 || h > 2048 {
		if w >= h {
			h = max(1, h*2048/w)
			w = 2048
		} else {
			w = max(1, w*2048/h)
			h = 2048
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, src.At(src.Bounds().Min.X+x*cfg.Width/w, src.Bounds().Min.Y+y*cfg.Height/h))
		}
	}
	out := &boundedBuffer{max: MaxBytes}
	if err = png.Encode(out, dst); err != nil {
		return nil, 0, 0, ErrTooLarge
	}
	return out.Bytes(), w, h, nil
}
