package activities

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// Keep the real ChatStep lifecycle and request builder; replace storage and
// inference only. Earlier-step results are recorded fixtures, not new tool work.
type budgetWebRepository struct {
	ownershipRepository
	WebRepository
	turn    chat.Turn
	history []chat.Exchange
	prior   *chat.ChatModelResult
	saved   *chat.ChatModelResult
}

func (r *budgetWebRepository) GetTurn(context.Context, chat.TurnRef) (chat.Turn, error) {
	return r.turn, nil
}
func (r *budgetWebRepository) RecentChatExchanges(context.Context, chat.TurnRef) ([]chat.Exchange, error) {
	return r.history, nil
}
func (r *budgetWebRepository) GetChatModelResult(_ context.Context, ref chat.TurnRef) (chat.ChatModelResult, error) {
	if r.prior != nil && ref.Step == r.prior.Step {
		return *r.prior, nil
	}
	if r.saved != nil && ref.Step == r.saved.Step {
		return *r.saved, nil
	}
	return chat.ChatModelResult{}, storage.ErrNotFound
}
func (r *budgetWebRepository) SaveChatModelResult(_ context.Context, result chat.ChatModelResult) error {
	r.saved = &result
	return nil
}
func (r *budgetWebRepository) PrepareWebActions(context.Context, chat.TurnRef) (chat.ModelStepResult, error) {
	return chat.ModelStepResult{}, nil
}
func (r *budgetWebRepository) WebToolResult(_ context.Context, _ chat.ConversationID, id string) (chat.WebToolResult, error) {
	return chat.WebToolResult{Text: "recorded result " + id}, nil
}

type budgetWebModel struct{ request *llm.Request }

func (m *budgetWebModel) Chat(_ context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.request = &r
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "answer"}}, nil
}

func TestChatStepEvictsWholeOptionalExchangesForWebOverhead(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		name := "prompt_and_schemas"
		if withTools {
			name = "current_turn_tool_group"
		}
		t.Run(name, func(t *testing.T) {
			ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
			repo := &budgetWebRepository{turn: chat.Turn{Model: "qwen3.5:9b", Text: "latest"}, history: []chat.Exchange{{User: strings.Repeat("u", 9000), Assistant: strings.Repeat("a", 2000)}}}
			if withTools {
				repo.prior = &chat.ChatModelResult{TurnRef: ref, Text: "checking two approved sources", Calls: []chat.WebModelCall{{ID: uuid.NewString(), Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com/"}`)}, {ID: uuid.NewString(), Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.org/"}`)}}}
				ref.Step = 1
			}
			model := &budgetWebModel{}
			a := ChatActivities{Repository: repo, Model: model}
			_, err := a.ChatStep(context.Background(), ref)
			require.NoError(t, err, "optional history must not make a short follow-up invalid")
			require.NotNil(t, model.request)
			req := *model.request
			require.Len(t, req.Tools, 2)
			require.Contains(t, req.Messages[0].Content, "limited")
			require.Equal(t, "latest", req.Messages[1].Content)
			require.True(t, repo.saved.LimitedHistory)
			require.Equal(t, 16384, req.ContextWindow)
			require.Equal(t, 2000, req.MaxOutputTokens)
			if !withTools {
				require.Len(t, req.Messages, 2)
			} else {
				require.Len(t, req.Messages, 5)
				require.Equal(t, "assistant", req.Messages[2].Role)
				require.Len(t, req.Messages[2].ToolCalls, 2)
				for i := 0; i < 2; i++ {
					require.Equal(t, "tool", req.Messages[3+i].Role)
					require.Equal(t, req.Messages[2].ToolCalls[i].ID, req.Messages[3+i].ToolCallID)
					require.Equal(t, "recorded result "+repo.prior.Calls[i].ID, req.Messages[3+i].Content)
				}
			}
		})
	}
}
func TestChatStepRetainsNewestFittingWholeExchangeAndProvenance(t *testing.T) {
	ref := chat.TurnRef{TurnID: chat.TurnID(uuid.NewString())}
	repo := &budgetWebRepository{turn: chat.Turn{Model: "qwen3.5:9b", Text: "correction", ReplacementTarget: chat.TurnID(uuid.NewString()), OriginalText: "original prompt", OriginalAttachmentIDs: []chat.AttachmentID{chat.AttachmentID(uuid.NewString())}}, history: []chat.Exchange{{User: "newest question", Assistant: "newest answer"}, {User: strings.Repeat("u", 9000), Assistant: strings.Repeat("a", 2000)}}}
	model := &budgetWebModel{}
	a := ChatActivities{Repository: repo, Model: model}
	_, err := a.ChatStep(context.Background(), ref)
	require.NoError(t, err)
	req := *model.request
	require.Len(t, req.Messages, 6)
	require.Equal(t, "newest question", req.Messages[1].Content)
	require.Equal(t, "newest answer", req.Messages[2].Content)
	require.Contains(t, req.Messages[3].Content, "original prompt")
	require.Contains(t, req.Messages[3].Content, string(repo.turn.OriginalAttachmentIDs[0]))
	require.Contains(t, req.Messages[4].Content, "not authorized")
	require.Equal(t, "correction", req.Messages[5].Content)
}
func TestChatStepRejectsOversizedMandatoryWebContext(t *testing.T) {
	for _, kind := range []string{"prompt", "escaped_prompt", "tool_group", "provenance"} {
		t.Run(kind, func(t *testing.T) {
			ref := chat.TurnRef{TurnID: chat.TurnID(uuid.NewString())}
			repo := &budgetWebRepository{turn: chat.Turn{Model: "qwen3.5:9b", Text: "latest"}, history: []chat.Exchange{{User: "optional", Assistant: "answer"}}}
			switch kind {
			case "prompt":
				repo.turn.Text = strings.Repeat("x", 12000)
			case "escaped_prompt":
				repo.turn.Text = strings.Repeat("<", 2100)
			case "provenance":
				repo.turn.ReplacementTarget = chat.TurnID(uuid.NewString())
				repo.turn.OriginalText = strings.Repeat("p", 12000)
			case "tool_group":
				repo.prior = &chat.ChatModelResult{TurnRef: ref, Text: strings.Repeat("m", 12000), Calls: []chat.WebModelCall{{ID: uuid.NewString(), Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.com/"}`)}}}
				ref.Step = 1
			}
			model := &budgetWebModel{}
			a := ChatActivities{Repository: repo, Model: model}
			_, err := a.ChatStep(context.Background(), ref)
			var app *temporal.ApplicationError
			require.ErrorAs(t, err, &app)
			require.Equal(t, "InvalidRequest", app.Type())
			require.True(t, app.NonRetryable())
			require.Nil(t, model.request, "must fail before inference, not truncate mandatory context")
			require.Nil(t, repo.saved)
		})
	}
}
