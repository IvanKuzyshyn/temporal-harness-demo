package attachments

import (
	"bytes"
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type imageRepository struct {
	*memoryRepository
	approved bool
	lookups  int
}

func (r *imageRepository) AuthorizeAttachment(context.Context, chat.ApprovalScope, chat.AttachmentID) error {
	if !r.approved {
		return storage.ErrConflict
	}
	return nil
}
func (r *imageRepository) GetAttachment(ctx context.Context, c chat.ConversationID, id chat.AttachmentID) (chat.Attachment, error) {
	r.lookups++
	return r.memoryRepository.GetAttachment(ctx, c, id)
}
func TestImageBytesResolvedOnlyAfterApproval(t *testing.T) {
	s, repo, c := fixtureStore(t)
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 10))))
	file, e := s.Put(context.Background(), c, bytes.NewReader(b.Bytes()), UploadMetadata{Name: "a.png", MediaType: "image/png"})
	require.NoError(t, e)
	r := &imageRepository{memoryRepository: repo}
	s.repo = r
	ref := ImageReference{AttachmentID: file.ID, DerivedKey: "model-png-v1", Checksum: file.Checksum}
	scope := chat.ApprovalScope{ConversationID: c, ContentRef: file.Checksum}
	_, e = s.ResolveImage(context.Background(), scope, ref)
	require.ErrorIs(t, e, storage.ErrConflict)
	require.Zero(t, r.lookups, "authorization before original metadata/cache lookup")
	r.approved = true
	out, e := s.ResolveImage(context.Background(), scope, ref)
	require.NoError(t, e)
	require.Equal(t, 20, out.Width)
	require.Equal(t, 10, out.Height)
	_, e = s.ResolveImage(context.Background(), scope, ImageReference{AttachmentID: file.ID, DerivedKey: "../../original", Checksum: file.Checksum})
	require.Error(t, e)
	ref.Checksum = strings.Repeat("0", 64)
	_, e = s.ResolveImage(context.Background(), scope, ref)
	require.Error(t, e)
	require.NoError(t, os.WriteFile(filepath.Join(repo.root, file.StorageKey), []byte("tampered"), 0600))
	ref.Checksum = file.Checksum
	_, e = s.ResolveImage(context.Background(), scope, ref)
	require.Error(t, e)
}
func TestImageDimensionsBound(t *testing.T) {
	for _, size := range []image.Point{{4096, 1024}, {8193, 1}, {5000, 5000}} {
		var b bytes.Buffer
		require.NoError(t, png.Encode(&b, image.NewGray(image.Rectangle{Max: size})))
		out, w, h, e := prepareRaster(b.Bytes())
		if size.X == 4096 {
			require.NoError(t, e)
			require.Equal(t, 2048, w)
			require.Equal(t, 512, h)
			cfg, format, e := image.DecodeConfig(bytes.NewReader(out))
			require.NoError(t, e)
			require.Equal(t, "png", format)
			require.Equal(t, w, cfg.Width)
		} else {
			require.Error(t, e)
		}
	}
}
func TestSVGNoExternalResources(t *testing.T) {
	realParser(t)
	p := ContainerParser{}
	good := `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="16"><rect width="32" height="16" fill="red"/></svg>`
	b, e := p.Rasterize(context.Background(), []byte(good))
	require.NoError(t, e)
	cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
	require.NoError(t, e)
	require.Equal(t, "png", format)
	require.Equal(t, 32, cfg.Width)
	for _, body := range []string{`<image href="http://127.0.0.1:9/x"/>`, `<script>alert(1)</script>`, `<text font-family="external">x</text>`, `<rect fill="url(file:///etc/passwd)"/>`, `<use href="#x"/>`, `<foreignObject/>`} {
		_, e = p.Rasterize(context.Background(), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="32" height="16">`+body+`</svg>`))
		require.Error(t, e, body)
	}
	_, e = p.Rasterize(context.Background(), []byte(`<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]>`+good))
	require.Error(t, e)
}
