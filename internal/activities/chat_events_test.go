package activities

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type displayRepository struct {
	ownershipRepository
	mu     sync.Mutex
	events []chat.Event
}

func (r *displayRepository) AppendEvent(_ context.Context, e chat.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}
func (r *displayRepository) SaveChatModelResult(context.Context, chat.ChatModelResult) error {
	return nil
}

type displayModel struct{}

func (displayModel) Chat(_ context.Context, _ llm.Request, on func(llm.Delta) error) (llm.Response, error) {
	for i := 0; i < 300; i++ {
		if err := on(llm.Delta{Text: "x"}); err != nil {
			return llm.Response{}, err
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "answer"}}, nil
}
func TestChatDraftUTF8BoundIsVisible(t *testing.T) {
	r := &displayRepository{}
	ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
	add, finish := chatDeltas(context.Background(), r, ref, 1)
	add(strings.Repeat("a", 7999) + "é")
	add("must not replace truncated suffix")
	finish()
	var data struct {
		Text    string
		Bounded bool
	}
	require.NoError(t, json.Unmarshal(r.events[len(r.events)-1].Payload, &data))
	require.True(t, utf8.ValidString(data.Text))
	require.Equal(t, strings.Repeat("a", 7999), data.Text)
	require.True(t, data.Bounded)
}
func TestChatModelBatchesAttemptIdentifiedDrafts(t *testing.T) {
	r := &displayRepository{}
	ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString()), Step: 2}
	a := ChatActivities{Repository: r, Model: displayModel{}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, a.ChatModel(ctx, ref))
	require.NotEmpty(t, r.events)
	require.Less(t, len(r.events), 10, "not a transaction per token")
	last := r.events[len(r.events)-1]
	require.Equal(t, ref.ConversationID, last.ConversationID)
	require.Equal(t, ref.TurnID, last.TurnID)
	require.Equal(t, 2, last.LogicalStep)
	require.Equal(t, 1, last.Attempt)
	require.Equal(t, "message.delta", last.Type)
	var data struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(last.Payload, &data))
	require.Len(t, data.Text, 300)
}
