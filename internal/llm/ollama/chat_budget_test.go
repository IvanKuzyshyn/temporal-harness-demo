package ollama

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatBudgetOptionsAndOriginalModel(t *testing.T) {
	var body struct {
		Model   string
		Options map[string]int
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
	}))
	defer s.Close()
	m, err := New(s.URL)
	require.NoError(t, err)
	_, err = m.Chat(context.Background(), llm.Request{Model: "gemma4:e4b", ContextWindow: 16384, MaxOutputTokens: 2000}, nil)
	require.NoError(t, err)
	require.Equal(t, "gemma4:e4b", body.Model)
	require.Equal(t, 16384, body.Options["num_ctx"])
	require.Equal(t, 2000, body.Options["num_predict"])
}
