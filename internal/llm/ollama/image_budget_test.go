package ollama

import (
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/ollama/ollama/model/renderers"
	"github.com/stretchr/testify/require"
)

func TestNarrowImageNearBudgetAdmission(t *testing.T) {
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b"} {
		t.Run(model, func(t *testing.T) {
			req := profileRequest(model)
			lo, hi := 1, 4096
			for lo < hi {
				mid := (lo + hi + 1) / 2
				req.Messages[1].Content = strings.Repeat("x", mid)
				if _, err := imageEnvelope(req); err == nil {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			require.Greater(t, lo, 3000)
			require.Less(t, lo, 4096)
			req.Messages[1].Content = strings.Repeat("x", lo)
			wire, e := imageEnvelope(req)
			require.NoError(t, e)
			_, renderer, tokens := imageIdentity(model)
			prompt, e := renderers.RenderWithRenderer(renderer, wire.Messages, nil, wire.Think)
			require.NoError(t, e)
			total := 3*len(prompt) + 512 + tokens + 2000 + 2384
			require.LessOrEqual(t, total, 16384)
			require.Greater(t, total, 16381)
			req.Messages[1].Content += "x"
			_, e = imageEnvelope(req)
			require.ErrorContains(t, e, "context budget exceeded")
			req.Messages[1].Content = "translate"
			req.Image.Reference.DerivedKey = "model-png-v1"
			req.Messages[1].Images = []llm.ImageReference{req.Image.Reference}
			_, e = imageEnvelope(req)
			require.Error(t, e, "a prior unqualified raster key cannot select the new profile")
			t.Logf("model=%s maximum repeated-ASCII instruction=%d bytes, full bound=%d, next byte rejected before provider I/O", model, lo, total)
		})
	}
}
