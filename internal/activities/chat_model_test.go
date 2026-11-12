package activities

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestReplacementContextRetainsOriginalProvenanceWithoutAttachmentBytes(t *testing.T) {
	turn := chat.Turn{Model: "qwen3.5:9b", Text: "correcting instruction", ReplacementTarget: "00000000-0000-4000-8000-000000000001", OriginalText: "original instruction", OriginalAttachmentIDs: []chat.AttachmentID{"00000000-0000-4000-8000-000000000002"}}
	req, err := chatRequest(turn, nil)
	require.NoError(t, err)
	require.Len(t, req.Messages, 4)
	require.Contains(t, req.Messages[1].Content, "original instruction")
	require.Contains(t, req.Messages[1].Content, "00000000-0000-4000-8000-000000000002")
	require.Contains(t, req.Messages[2].Content, "not authorized")
	require.Equal(t, "correcting instruction", req.Messages[3].Content)
	require.Empty(t, req.Tools)
	turn.OriginalText = strings.Repeat("x", 12000)
	_, err = chatRequest(turn, nil)
	require.Error(t, err, "original provenance counts against input budget")
}

func TestChatContextBudgetAndWholeExchanges(t *testing.T) {
	// Removing the measured budget or including queued/partial history must fail.
	req, err := chatRequest(chat.Turn{Model: "qwen3.5:9b", Text: "latest"}, []chat.Exchange{{User: "recent", Assistant: "answer"}, {User: strings.Repeat("x", 12000), Assistant: "too large"}})
	require.NoError(t, err)
	require.Len(t, req.Messages, 4)
	require.Equal(t, "recent", req.Messages[1].Content)
	require.Equal(t, "latest", req.Messages[3].Content)
	require.Equal(t, "qwen3.5:9b", req.Model)
	require.Empty(t, req.Tools)
	require.Empty(t, req.Scenario)
	require.Contains(t, req.Messages[0].Content, "limited")
	require.Equal(t, 16384, req.ContextWindow)
	require.Equal(t, 2000, req.MaxOutputTokens)
	_, err = chatRequest(chat.Turn{Model: "gemma4:e4b", Text: strings.Repeat("😀", 3000)}, nil)
	require.Error(t, err, "UTF-8 bytes, not character count")
}
