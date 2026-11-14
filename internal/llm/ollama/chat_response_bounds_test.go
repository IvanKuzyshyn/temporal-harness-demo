package ollama

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBoundedChatRejectsUnrequestedTools(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"message":{"tool_calls":[{"function":{"name":"read_file","arguments":{}}}]},"done":true}` + "\n"))
	}))
	defer s.Close()
	m, err := New(s.URL)
	require.NoError(t, err)
	_, err = m.Chat(context.Background(), llm.Request{Model: "qwen3.5:9b", MaxResponseBytes: 65536}, nil)
	var invalid *InvalidRequestError
	require.ErrorAs(t, err, &invalid)
}
func TestBoundedChatRejectsOversizedResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"message":{"content":"too much"},"done":true}` + "\n"))
	}))
	defer s.Close()
	m, err := New(s.URL)
	require.NoError(t, err)
	_, err = m.Chat(context.Background(), llm.Request{Model: "qwen3.5:9b", MaxResponseBytes: 3}, nil)
	var invalid *InvalidRequestError
	require.ErrorAs(t, err, &invalid)
}
