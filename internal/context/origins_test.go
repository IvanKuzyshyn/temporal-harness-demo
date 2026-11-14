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

func originsFixture(t *testing.T) (*Builder, *summaryRepo, string, string) {
	t.Helper()
	b, r, _ := summaryFixture()
	r.state.Older = nil
	r.state.Total = 2
	r.state.Summary = &chat.Summary{ID: uuid.NewString(), Model: r.state.Model, Version: 1, CoveredSequence: 2, Text: "Assistant said storage was unavailable; no facts retained.", Budget: []byte(`{"exchanges":1,"sourceLimited":true}`)}
	r.state.Recent = []chat.Exchange{{Sequence: 10, User: "Later human correction: use the revised value.", Assistant: "Correction received."}}
	user, assistant := "User supplied the cedar sign.", "I refused to remember that."
	raw, e := json.Marshal(r.state)
	require.NoError(t, e)
	var data map[string]any
	require.NoError(t, json.Unmarshal(raw, &data))
	turn := uuid.NewString()
	quote := func(role, text string) map[string]any {
		return map[string]any{"sourceId": uuid.NewString(), "speaker": role, "turnId": turn, "sequence": 2, "textHash": strings.Repeat("a", 64), "bytes": len(text), "text": text}
	}
	data["Quotations"] = []any{map[string]any{"User": quote("user", user), "Assistant": quote("assistant", assistant)}}
	data["QuotationTotal"] = 1
	raw, e = json.Marshal(data)
	require.NoError(t, e)
	r.state = chat.ContextHistory{}
	require.NoError(t, json.Unmarshal(raw, &r.state))
	return b, r, user, assistant
}
func originCoverage(t *testing.T, out ModelContext) map[string]any {
	t.Helper()
	raw, e := json.Marshal(out.Coverage)
	require.NoError(t, e)
	var c map[string]any
	require.NoError(t, json.Unmarshal(raw, &c))
	require.Contains(t, c, "quotations")
	return c["quotations"].(map[string]any)
}
func TestStructuredOriginsSeparateUserTextRefusalAndGeneratedNotes(t *testing.T) {
	b, r, user, assistant := originsFixture(t)
	before, _ := json.Marshal(r.state)
	c, turn := chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString())
	out, e := b.Build(context.Background(), c, turn, DefaultBudget())
	require.NoError(t, e)
	require.Contains(t, out.Request.Messages[1].Content, `"userQuotations"`)
	raw := out.Request.Messages[1].Content
	require.Less(t, strings.Index(raw, `"assistantGeneratedNotes"`), strings.Index(raw, `"userQuotations"`), "preserve the successful probe's source-field composition order")
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw[strings.IndexByte(raw, '{'):]), &data))
	var users, replies []map[string]any
	require.NoError(t, json.Unmarshal(data["userQuotations"], &users))
	require.NoError(t, json.Unmarshal(data["assistantReplies"], &replies))
	require.Len(t, users, 1)
	require.Equal(t, user, users[0]["text"])
	require.Equal(t, "user", users[0]["speaker"])
	require.NotEmpty(t, users[0]["sourceId"])
	require.Len(t, replies, 1)
	require.Equal(t, assistant, replies[0]["text"])
	require.Equal(t, "assistant", replies[0]["speaker"])
	require.Contains(t, string(data["assistantGeneratedNotes"]), r.state.Summary.Text)
	require.Equal(t, r.state.Recent[0].User, out.Request.Messages[2].Content, "late human correction follows older sources")
	require.EqualValues(t, 0, originCoverage(t, out)["omitted"])
	again, e := b.Build(context.Background(), c, turn, DefaultBudget())
	require.NoError(t, e)
	require.Equal(t, out.Request, again.Request)
	require.Equal(t, out.Coverage, again.Coverage)
	after, _ := json.Marshal(r.state)
	require.Equal(t, before, after, "source records are immutable")
}
func TestStructuredOriginsWholePairsShareCurrentAllowance(t *testing.T) {
	b, _, user, assistant := originsFixture(t)
	// Less than one whole pair: do not retain just a user request and lose refusal.
	budget := DefaultBudget()
	budget.Excerpts = 3000 + len(user) + len(assistant) - 1
	current, _ := json.Marshal(chat.WebToolResult{Text: strings.Repeat("z", 3000)})
	b.Mandatory.Messages = append(b.Mandatory.Messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "current", Name: "web_fetch", Args: []byte(`{}`)}}}, llm.Message{Role: "tool", ToolCallID: "current", Content: string(current)})
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), budget)
	require.NoError(t, e)
	require.NotContains(t, requestText(out.Request), user)
	require.NotContains(t, requestText(out.Request), assistant)
	require.EqualValues(t, 1, originCoverage(t, out)["omitted"])
}
func TestStructuredOriginsTotalPressureAndRecentDedup(t *testing.T) {
	b, r, user, _ := originsFixture(t)
	b.Mandatory.Messages[1].Content = strings.Repeat("m", 11100)
	out, e := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.NotContains(t, requestText(out.Request), user)
	require.EqualValues(t, 1, originCoverage(t, out)["omitted"])
	require.LessOrEqual(t, out.Accounting.InputTokens, 12000)
	b, r, user, assistant := originsFixture(t)
	r.state.Summary = nil
	r.state.Total = 1
	r.state.Recent = []chat.Exchange{{Sequence: 2, User: user, Assistant: assistant}}
	out, e = b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, e)
	require.Equal(t, 1, strings.Count(requestText(out.Request), user))
	require.EqualValues(t, 1, originCoverage(t, out)["deduplicated"])
}
