package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func request() llm.Request {
	return llm.Request{Model: "qwen3.5:9b", ContextWindow: 16384, MaxOutputTokens: 2000, MaxResponseBytes: 16000, Messages: []llm.Message{{Role: "system", Content: "History is limited."}, {Role: "user", Content: "latest"}}}
}
func TestBudgetIncludesSchemasAndImages(t *testing.T) {
	r := request()
	r.Tools = []llm.ToolDefinition{{Name: "tool", Description: strings.Repeat("schema", 3000), Parameters: []byte(`{"type":"object"}`)}}
	_, err := Assemble(r, nil, DefaultBudget())
	require.Error(t, err)
	ref := llm.ImageReference{AttachmentID: chat.AttachmentID(uuid.NewString()), Checksum: strings.Repeat("a", 64), DerivedKey: llm.NarrowImageProfile}
	for _, model := range chat.EnabledModels() {
		img, err := ollama.ImageRequest(model, "Translate.", ref)
		require.NoError(t, err)
		out, err := Assemble(img, nil, DefaultBudget())
		require.NoError(t, err)
		require.Positive(t, out.Accounting.ImageTokens)
		require.LessOrEqual(t, out.Accounting.Total, 16384)
		_, err = Assemble(img, [][]llm.Message{{{Role: "user", Content: "history"}, {Role: "assistant", Content: "answer"}}}, DefaultBudget())
		require.Error(t, err, "profile must not absorb history")
	}
}
func TestRecentSixStillBounded(t *testing.T) {
	var groups [][]llm.Message
	for i := 0; i < 8; i++ {
		groups = append(groups, []llm.Message{{Role: "user", Content: strings.Repeat("x", 2000)}, {Role: "assistant", Content: strings.Repeat("y", 2000)}})
	}
	out, err := Assemble(request(), groups, DefaultBudget())
	require.NoError(t, err)
	require.Less(t, out.Coverage.RecentExchanges, 6)
	require.Positive(t, out.Coverage.OmittedExchanges)
	require.LessOrEqual(t, out.Accounting.InputTokens, 12000)
	require.LessOrEqual(t, out.Accounting.Total, 16384)
}
func TestToolPairsNotSplit(t *testing.T) {
	r := request()
	r.Messages = append(r.Messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "a", Name: "fetch", Args: []byte(`{}`)}, {ID: "b", Name: "fetch", Args: []byte(`{}`)}}}, llm.Message{Role: "tool", ToolCallID: "a", Content: "first"}, llm.Message{Role: "tool", ToolCallID: "b", Content: "second"})
	out, err := Assemble(r, [][]llm.Message{{{Role: "user", Content: strings.Repeat("x", 12000)}, {Role: "assistant", Content: "old"}}}, DefaultBudget())
	require.NoError(t, err)
	require.Equal(t, r.Messages, out.Request.Messages)
	r.Messages = r.Messages[:len(r.Messages)-1]
	_, err = Assemble(r, nil, DefaultBudget())
	require.Error(t, err, "orphan tool group")
}
func TestReplacementContextProvenance(t *testing.T) {
	turn := chat.Turn{Model: "qwen3.5:9b", Text: "correction", ReplacementTarget: chat.TurnID(uuid.NewString()), OriginalText: "original", OriginalAttachmentIDs: []chat.AttachmentID{chat.AttachmentID(uuid.NewString())}}
	r, err := Mandatory(turn, "limited history")
	require.NoError(t, err)
	out, err := Assemble(r, [][]llm.Message{{{Role: "user", Content: "prior"}, {Role: "assistant", Content: "completed"}}}, DefaultBudget())
	require.NoError(t, err)
	require.Contains(t, out.Request.Messages[3].Content, "original")
	require.Contains(t, out.Request.Messages[3].Content, string(turn.OriginalAttachmentIDs[0]))
	require.Equal(t, "correction", out.Request.Messages[5].Content)
}

type summaryRepo struct {
	state chat.ContextHistory
	saved []chat.ContextSummaryWrite
	fail  error
}

func (r *summaryRepo) LoadContext(context.Context, chat.TurnRef) (chat.ContextHistory, error) {
	return r.state, nil
}
func (r *summaryRepo) CommitContextSummary(_ context.Context, w chat.ContextSummaryWrite) (chat.Summary, error) {
	if r.fail != nil {
		return chat.Summary{}, r.fail
	}
	r.saved = append(r.saved, w)
	r.state.Summary = &w.Summary
	var left []chat.Exchange
	for _, e := range r.state.Older {
		if e.Sequence > w.Summary.CoveredSequence {
			left = append(left, e)
		}
	}
	r.state.Older = left
	return w.Summary, nil
}

type summaryModel struct {
	calls int
	err   error
}

func (m *summaryModel) Chat(_ context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.calls++
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "Remember the blue kite. Sources are partial."}, Tokens: 20}, m.err
}
func summaryFixture() (*Builder, *summaryRepo, *summaryModel) {
	r := &summaryRepo{state: chat.ContextHistory{Model: "qwen3.5:9b", Total: 9, Older: []chat.Exchange{{User: "one", Assistant: "a", Sequence: 2}, {User: "two", Assistant: "b", Sequence: 5}, {User: "three", Assistant: "c", Sequence: 9}}}}
	m := &summaryModel{}
	return &Builder{Repository: r, Model: m, Mandatory: request(), AttemptID: uuid.NewString()}, r, m
}
func TestSummaryCoverageNoGapsOrDuplicates(t *testing.T) {
	b, r, _ := summaryFixture()
	out, err := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, err)
	require.Len(t, r.saved, 1)
	require.Equal(t, []int64{2, 5, 9}, r.saved[0].Sequences)
	require.EqualValues(t, 9, out.Coverage.SummaryBoundary)
	require.EqualValues(t, 1, out.Coverage.SummaryVersion)
	require.Contains(t, out.Request.Messages[1].Content, "blue kite")
}
func TestSummaryRetryIdempotent(t *testing.T) {
	b, r, m := summaryFixture()
	c := chat.ConversationID(uuid.NewString())
	turn := chat.TurnID(uuid.NewString())
	first, err := b.Build(context.Background(), c, turn, DefaultBudget())
	require.NoError(t, err)
	second, err := b.Build(context.Background(), c, turn, DefaultBudget())
	require.NoError(t, err)
	require.Equal(t, first.Request, second.Request)
	require.Equal(t, 1, m.calls)
	require.Len(t, r.saved, 1)
}
func TestSummaryFailureReportsReducedCoverage(t *testing.T) {
	b, r, m := summaryFixture()
	m.err = errors.New("model unavailable")
	out, err := b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.NoError(t, err)
	require.Equal(t, "generation_failed", out.Coverage.SummaryFailure)
	require.Empty(t, r.saved)
	require.Zero(t, out.Coverage.SummaryVersion)
	require.Contains(t, out.Request.Messages[0].Content, "omitted")
	m.err = nil
	r.fail = errors.New("database unavailable")
	_, err = b.Build(context.Background(), chat.ConversationID(uuid.NewString()), chat.TurnID(uuid.NewString()), DefaultBudget())
	require.ErrorContains(t, err, "database unavailable")
}
