package ollama

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Image admission is deliberately unqualified for BOTH exact tags. An opaque
// reference must not be silently dropped, interpreted as text-only success, or
// sent under a fallback model. This is a rejection test, not live vision proof.
func TestImageAdapterExactModel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"message":{"content":"not image analysis"},"done":true}` + "\n"))
	}))
	defer server.Close()
	m, e := New(server.URL)
	require.NoError(t, e)
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b", "qwen3.5:latest"} {
		_, e = m.Chat(context.Background(), llm.Request{Model: model, ContextWindow: 16384, MaxOutputTokens: 2000, Messages: []llm.Message{{Role: "user", Content: "translate", Images: []llm.ImageReference{{AttachmentID: "opaque", DerivedKey: "model-png-v1", Checksum: "opaque"}}}}}, nil)
		require.Error(t, e, model)
	}
	require.Zero(t, calls.Load(), "no unqualified or fallback HTTP requests")
}
