package activities

import (
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDocumentRequestBudgetBeforeInference(t *testing.T) {
	for _, model := range chat.EnabledModels() {
		req, e := documentRequest(model, strings.Repeat("i", 4096), strings.Repeat("x", 4000))
		require.NoError(t, e)
		require.Equal(t, model, req.Model)
		require.Empty(t, req.Tools)
		cost := 0
		for _, m := range req.Messages {
			raw, e := json.Marshal(m)
			require.NoError(t, e)
			cost += len(raw) + 128
		}
		require.LessOrEqual(t, cost, 12000)
		require.LessOrEqual(t, cost+req.MaxOutputTokens+2384, req.ContextWindow)
		_, e = documentRequest(model, strings.Repeat("\u2028", 1365), strings.Repeat("<", 4000))
		require.Error(t, e, "JSON escaping/prompt/output reserve cannot be ignored")
	}
	_, e := documentRequest("qwen3.5:latest", "analyze", "x")
	require.Error(t, e)
}
func TestDocumentChunkByteCoverageBound(t *testing.T) {
	text := strings.Repeat("é", 70000)
	chunks := documentChunks(text, chat.DefaultDocumentBudget())
	require.Len(t, chunks, 32)
	for _, chunk := range chunks {
		require.True(t, utf8.ValidString(chunk))
		require.LessOrEqual(t, len(chunk), 4000)
	}
	require.Equal(t, text[:128000], strings.Join(chunks, ""))
	require.Less(t, len(strings.Join(chunks, "")), len(text))
}
