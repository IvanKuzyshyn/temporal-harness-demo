package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/stretchr/testify/require"
)

// Qualification-only experiments, not application admission. Fixed known small
// fixture, one image/request, tiny prompt, exact tags, output/deadline caps and
// serial calls. This does NOT change imageAdmissionQualified or admit arbitrary
// text+image/aspect ratios. Semantic outcomes are recorded, not hidden as success.
func TestTask9LiveSmallImageExperiments(t *testing.T) {
	if os.Getenv("WEBCHAT_TASK9_LIVE") != "1" {
		t.Skip("explicit bounded live experiments only")
	}
	evidence := os.Getenv("WEBCHAT_TASK9_EVIDENCE")
	require.NotEmpty(t, evidence)
	file, e := os.OpenFile(evidence, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, e)
	defer file.Close()
	pngData, e := os.ReadFile("testdata/bonjour.png")
	require.NoError(t, e)
	img, _, e := image.Decode(bytes.NewReader(pngData))
	require.NoError(t, e)
	require.Equal(t, image.Rect(0, 0, 640, 160), img.Bounds())
	var jpegData bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpegData, img, &jpeg.Options{Quality: 95}))
	// Convert the known fixture's dark pixel runs to a static-vector fixture.
	// No font loading, handwritten SVG parser, text element or active resource.
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="640" height="160"><rect width="640" height="160" fill="white"/>`)
	dark := func(x, y int) bool { r, g, b, _ := img.At(x, y).RGBA(); return r+g+b < 3*32768 }
	for y := 0; y < 160; y++ {
		for x := 0; x < 640; {
			if !dark(x, y) {
				x++
				continue
			}
			start := x
			for x < 640 && dark(x, y) {
				x++
			}
			fmt.Fprintf(&svg, `<rect x="%d" y="%d" width="%d" height="1"/>`, start, y, x-start)
		}
	}
	svg.WriteString(`</svg>`)
	require.LessOrEqual(t, svg.Len(), 1048576)
	raster, e := (attachments.ContainerParser{}).Rasterize(context.Background(), []byte(svg.String()))
	require.NoError(t, e)
	enc := json.NewEncoder(file)
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b"} {
		for _, fixture := range []struct {
			name string
			data []byte
		}{{"png", pngData}, {"jpeg", jpegData.Bytes()}, {"safe-svg-raster", raster}} {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			client := &http.Client{Timeout: 90 * time.Second}
			res := qualifyChat(ctx, client, "http://127.0.0.1:11434", model, "image", fixture.data)
			cancel()
			client.CloseIdleConnections()
			record := struct {
				Fixture                 string `json:"fixture"`
				QualificationOnly       bool   `json:"qualificationOnly"`
				ImageAdmissionQualified bool   `json:"imageAdmissionQualified"`
				Result                  result `json:"result"`
			}{Fixture: fixture.name, QualificationOnly: true, ImageAdmissionQualified: false, Result: res}
			require.NoError(t, enc.Encode(record))
			require.NoError(t, file.Sync())
			t.Logf("exact=%s fixture=%s semantics=%s prompt_tokens=%d error=%q text=%q", model, fixture.name, res.ImageSemantics, res.PromptTokens, res.Error, res.Text)
		}
	}
}
