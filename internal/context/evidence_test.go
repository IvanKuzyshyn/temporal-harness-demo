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

func evidenceBuilder(t *testing.T, text string) (*Builder, string) {
	t.Helper()
	b, r, _ := summaryFixture()
	r.state = chat.ContextHistory{}
	id := uuid.NewString()
	raw, e := json.Marshal(map[string]any{"Model": "qwen3.5:9b", "Total": 2, "Summary": chat.Summary{Model: "qwen3.5:9b", Version: 2, CoveredSequence: 2, Text: "Assistant interpretation: no visible text was found.", Budget: []byte(`{"exchanges":1,"sourceLimited":true}`)}, "Recent": []chat.Exchange{{Sequence: 4, User: "Human correction: this is a draft, not final.", Assistant: "Noted."}}, "EvidenceTotal": 1, "Evidence": []any{map[string]any{"source": map[string]any{"scope": chat.ApprovalScope{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString()), ToolCallID: id, ToolName: "file_upload", ContentRef: strings.Repeat("b", 64), ActionHash: strings.Repeat("a", 64)}, "sequence": 2, "attachmentId": uuid.NewString(), "argumentsHash": strings.Repeat("c", 64), "resultHash": strings.Repeat("d", 64)}, "arguments": map[string]any{"instruction": "Translate the approved image."}, "result": chat.WebToolResult{Text: text, ExtractionStatus: "partial", Truncated: true, Error: "partial source", Coverage: []chat.DocumentCoverage{{Index: 0, StartByte: 0, EndByte: 100, SummaryTruncated: true}}}}}})
	require.NoError(t, e)
	require.NoError(t, json.Unmarshal(raw, &r.state))
	return b, id
}
func requestText(r llm.Request) string { raw, _ := json.Marshal(r.Messages); return string(raw) }
func TestCanonicalEvidenceSurvivesContradictorySummary(t *testing.T) {
	b, id := evidenceBuilder(t, "A recorded observation")
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	text := requestText(out.Request)
	require.Contains(t, text, "A recorded observation", "lossy interpretation must not be the only surviving source")
	require.Contains(t, text, id)
	require.Contains(t, text, strings.Repeat("b", 64))
	require.Contains(t, text, "Translate the approved image.")
	require.Contains(t, text, "no visible text was found")
	require.Contains(t, text, "Human correction")
	require.Contains(t, text, "not guaranteed factual truth")
	require.Contains(t, text, "partial source")
	require.Contains(t, text, "SummaryTruncated")
	raw, _ := json.Marshal(out.Coverage)
	require.Contains(t, string(raw), id)
	require.Contains(t, string(raw), `"omitted":0`)
	require.Contains(t, string(raw), `"sourceTruncated":true`)
	require.Contains(t, string(raw), `"extractionStatus":"partial"`)
	require.Contains(t, string(raw), `"sourceError":true`)
}
func TestCanonicalEvidenceBudgetAndCurrentDedup(t *testing.T) {
	b, id := evidenceBuilder(t, strings.Repeat("x", 4500))
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.Contains(t, requestText(out.Request), strings.Repeat("x", 4000))
	require.NotContains(t, requestText(out.Request), strings.Repeat("x", 4001))
	require.LessOrEqual(t, out.Accounting.Total, 16384)
	raw, _ := json.Marshal(out.Coverage)
	require.Contains(t, string(raw), `"excerptTruncated":true`)
	// A current tool group is mandatory and already represents this exact call.
	b.Mandatory.Messages = append(b.Mandatory.Messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: "file_upload", Args: []byte(`{}`)}}}, llm.Message{Role: "tool", ToolCallID: id, Content: "current canonical result"})
	out, e = b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.NotContains(t, requestText(out.Request), strings.Repeat("x", 100))
	require.Contains(t, requestText(out.Request), "current canonical result")
	raw, _ = json.Marshal(out.Coverage)
	require.Contains(t, string(raw), `"deduplicated":1`)
}
func TestCanonicalEvidenceSharesCurrentExcerptAllowance(t *testing.T) {
	b, _ := evidenceBuilder(t, strings.Repeat("x", 1000))
	original := b.Repository.(*summaryRepo).state.Evidence[0].Result
	current, _ := json.Marshal(chat.WebToolResult{Text: strings.Repeat("z", 3500)})
	b.Mandatory.Messages = append(b.Mandatory.Messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: uuid.NewString(), Name: "web_fetch", Args: []byte(`{}`)}}}, llm.Message{Role: "tool", ToolCallID: "temporary", Content: string(current)})
	b.Mandatory.Messages[len(b.Mandatory.Messages)-1].ToolCallID = b.Mandatory.Messages[len(b.Mandatory.Messages)-2].ToolCalls[0].ID
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.Contains(t, requestText(out.Request), strings.Repeat("x", 500))
	require.NotContains(t, requestText(out.Request), strings.Repeat("x", 501))
	require.Equal(t, 500, out.Coverage.Evidence.Included[0].ExcerptBytes)
	require.Equal(t, original, b.Repository.(*summaryRepo).state.Evidence[0].Result, "canonical result must not be mutated by admission")
}
func TestCanonicalEvidenceOmittedUnderTotalPressure(t *testing.T) {
	b, _ := evidenceBuilder(t, strings.Repeat("x", 4000))
	budget := DefaultBudget()
	budget.Input = 2000
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), budget)
	require.NoError(t, e)
	require.NotContains(t, requestText(out.Request), strings.Repeat("x", 100))
	require.LessOrEqual(t, out.Accounting.InputTokens, 2000)
	raw, _ := json.Marshal(out.Coverage)
	require.Contains(t, string(raw), `"omitted":1`)
}
