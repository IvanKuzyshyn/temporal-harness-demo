package attachments

import (
	"bytes"
	"context"
	"image"
)

// The same pinned container and kill/join protocol as document extraction:
// 10s execution, 512MiB memory, 1 CPU/8 CPU seconds, 32 pids, no network/mounts,
// read-only filesystem, no capabilities, non-root and no new privileges.
func (p ContainerParser) Rasterize(ctx context.Context, b []byte) ([]byte, error) {
	out, err := p.run(ctx, b, "image/svg+xml", false, true)
	if err != nil {
		return nil, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out.Raster))
	if err != nil || format != "png" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
		return nil, ErrInvalid
	}
	return out.Raster, nil
}
