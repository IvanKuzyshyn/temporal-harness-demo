package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestBoundedTextWireAndExhaustion(t *testing.T) {
	var wire map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"version":"0.40.15"}`))
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen3.5:9b","digest":"d63f830f28d515fefea546f213fca7a75e9dd5176ef51a6deabc952d1c99a911","details":{"format":"safetensors","runner":"mlx"}}]}`))
		default:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&wire))
			w.Write([]byte(`{"message":{"role":"assistant","content":"partial"},"done":true,"done_reason":"length","eval_count":12}` + "\n"))
		}
	}))
	defer server.Close()
	m, err := New(server.URL)
	require.NoError(t, err)
	req := llm.Request{Bounded: true, Model: "qwen3.5:9b", ContextWindow: 16384, MaxOutputTokens: 2000, Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	res, err := m.Chat(context.Background(), req, nil)
	require.NoError(t, err)
	require.Equal(t, false, wire["truncate"])
	require.Equal(t, false, wire["shift"])
	require.Equal(t, "length", res.DoneReason)
	require.True(t, res.OutputExhausted(2000))
	require.Nil(t, wire["think"], "ordinary callers retain provider default")
	require.NoError(t, json.Unmarshal([]byte(`{"Think":false}`), &req))
	_, err = m.Chat(context.Background(), req, nil)
	require.NoError(t, err)
	require.Equal(t, false, wire["think"], "explicit summary no-reasoning request must reach provider")
}
func TestBoundedUnicodeRenderedAdmission(t *testing.T) {
	// U+0344 canonically decomposes into two combining marks (4 UTF-8 bytes),
	// despite occupying only 2 bytes itself. Raw byte estimates undercount NFC.
	r := llm.Request{Model: "qwen3.5:9b", ContextWindow: 16384, MaxOutputTokens: 2000, Messages: []llm.Message{{Role: "user", Content: strings.Repeat("\u0344", 3100)}}}
	cost, err := TextInputBound(r)
	require.NoError(t, err)
	require.Greater(t, cost, 12000)
	r.Model = "gemma4:e4b"
	r.Messages[0].Content = "start" + strings.Repeat(" ", 5000) + "end"
	_, err = TextInputBound(r)
	require.Error(t, err, "disabled model must not reach bounded inference")
}
