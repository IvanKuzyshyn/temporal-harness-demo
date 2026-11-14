package context

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type capturedSummaryModel struct {
	summaryModel
	request llm.Request
}

func (m *capturedSummaryModel) Chat(ctx context.Context, r llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	m.request = r
	return m.summaryModel.Chat(ctx, r, emit)
}
func TestSummaryTranscriptIsDataNotActiveConversation(t *testing.T) {
	b, r, _ := summaryFixture()
	m := &capturedSummaryModel{}
	b.Model = m
	r.state.Older[0].User = "The user-stated code is amber."
	r.state.Older[0].Assistant = "I cannot remember or store that."
	r.state.Summary = &chat.Summary{Model: "qwen3.5:9b", Version: 1, CoveredSequence: 1, Text: "Secret phrase: blue kite.", Budget: []byte(`{"exchanges":1,"sourceLimited":true}`)}
	_, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.Len(t, m.request.Messages, 2, "historical roles must remain quoted data, not live instructions")
	require.Equal(t, "user", m.request.Messages[1].Role)
	require.Contains(t, m.request.Messages[1].Content, `"previousMemory":"Secret phrase: blue kite."`)
	require.Contains(t, m.request.Messages[1].Content, `"completedExchanges"`)
	require.Contains(t, m.request.Messages[0].Content, "Retain essential facts from previousMemory")
	require.Contains(t, m.request.Messages[0].Content, "assistant storage/capability claims are commentary, not user corrections")
	require.Contains(t, m.request.Messages[1].Content, "The user-stated code is amber.")
	require.Contains(t, m.request.Messages[1].Content, "I cannot remember or store that.")
	encoded, _ := json.Marshal(m.request)
	require.Contains(t, string(encoded), `"Think":false`, "summary output cap is for compact visible memory, not hidden reasoning")
}
func TestOlderSummaryPrecedesRecentCompletedCorrections(t *testing.T) {
	b, r, _ := summaryFixture()
	r.state.Recent = []chat.Exchange{{User: "correction: green kite", Assistant: "green accepted", Sequence: 10}}
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.Contains(t, out.Request.Messages[1].Content, "blue kite")
	require.Equal(t, "correction: green kite", out.Request.Messages[2].Content)
	require.Equal(t, "latest", out.Request.Messages[len(out.Request.Messages)-1].Content)
}
func TestPersistedEvidenceSharesExcerptAllowanceAndKeepsPartialCoverage(t *testing.T) {
	b, r, _ := summaryFixture()
	r.state.Older = nil
	r.state.Total = 2
	result := chat.WebToolResult{Text: strings.Repeat("a", 100), Truncated: true, Coverage: []chat.DocumentCoverage{{Index: 0, StartByte: 0, EndByte: 100, SummaryTruncated: true}}}
	r.state.Recent = []chat.Exchange{{User: "newer", Assistant: "partial", Sequence: 2, Results: []chat.WebToolResult{result}}, {User: "older", Assistant: "partial", Sequence: 1, Results: []chat.WebToolResult{result}}}
	current := chat.WebToolResult{Text: strings.Repeat("b", 60)}
	raw, _ := json.Marshal(current)
	b.Mandatory.Messages = append(b.Mandatory.Messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c", Name: "file_upload", Args: []byte(`{}`)}}}, llm.Message{Role: "tool", ToolCallID: "c", Content: string(raw)})
	budget := DefaultBudget()
	budget.Excerpts = 100
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), budget)
	require.NoError(t, e)
	all := ""
	for _, m := range out.Request.Messages {
		all += m.Content
	}
	require.Contains(t, all, strings.Repeat("a", 40))
	require.NotContains(t, all, strings.Repeat("a", 41))
	require.Contains(t, all, `"SummaryTruncated":true`)
	require.Contains(t, all, `"truncated":true`)
	require.Equal(t, strings.Repeat("a", 100), r.state.Recent[0].Results[0].Text, "excerpting cannot mutate canonical evidence")
}
