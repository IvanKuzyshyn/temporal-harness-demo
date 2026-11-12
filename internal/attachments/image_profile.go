package attachments

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

// ApprovedMetadata selects the content path only after the same immutable scope
// check used for original/cache reads. Filename and display events are irrelevant.
func (s *Store) ApprovedMetadata(ctx context.Context, scope chat.ApprovalScope, id chat.AttachmentID) (chat.Attachment, error) {
	r, ok := s.repo.(interface {
		AuthorizeAttachment(context.Context, chat.ApprovalScope, chat.AttachmentID) error
	})
	if !ok {
		return chat.Attachment{}, ErrInvalid
	}
	if err := r.AuthorizeAttachment(ctx, scope, id); err != nil {
		return chat.Attachment{}, err
	}
	a, err := s.repo.GetAttachment(ctx, scope.ConversationID, id)
	if err != nil {
		return a, err
	}
	if a.Checksum != scope.ContentRef || a.Status != "ready" {
		return chat.Attachment{}, ErrInvalid
	}
	return a, nil
}

// Called only with the already decoded/validated bounded model-png-v1 raster.
// Aspect-preserving downsample, no enlargement, opaque white letterbox: provider
// preprocessing always sees EXACTLY 640x160 regardless of source shape/content.
func narrowRaster(data []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalid
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	w, h := sw, sh
	if w > 640 {
		h = max(1, h*640/w)
		w = 640
	}
	if h > 160 {
		w = max(1, w*160/h)
		h = 160
	}
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			small.Set(x, y, src.At(x*sw/w, y*sh/h))
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, 640, 160))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	rect := image.Rect((640-w)/2, (160-h)/2, (640-w)/2+w, (160-h)/2+h)
	draw.Draw(dst, rect, small, image.Point{}, draw.Over)
	out := &boundedBuffer{max: MaxBytes}
	if err = png.Encode(out, dst); err != nil {
		return nil, ErrTooLarge
	}
	return out.Bytes(), nil
}
