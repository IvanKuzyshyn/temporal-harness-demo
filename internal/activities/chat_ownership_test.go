package activities

import (
	"context"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// The real transactional interleavings are tested against Postgres/Temporal.
// This seam isolates activity policy: adoption is NOT permission to mutate a
// winner, nor to treat this owner's changed payload as a successful retry.
type ownershipRepository struct {
	ChatRepository
	cached bool
	winner string
	reads  int
}

func (r *ownershipRepository) GetChatModelResult(_ context.Context, ref chat.TurnRef) (chat.ChatModelResult, error) {
	r.reads++
	if r.reads == 1 {
		return chat.ChatModelResult{}, storage.ErrNotFound
	}
	return chat.ChatModelResult{TurnRef: ref, AttemptID: r.winner, Text: "canonical"}, nil
}
func (r *ownershipRepository) GetTurn(_ context.Context, ref chat.TurnRef) (chat.Turn, error) {
	return chat.Turn{TurnRef: ref, Text: "hi", Model: "qwen3.5:9b"}, nil
}
func (r *ownershipRepository) RecentChatExchanges(context.Context, chat.TurnRef) ([]chat.Exchange, error) {
	return nil, nil
}
func (r *ownershipRepository) RegisterChatModelInvocation(context.Context, chat.TurnRef, string, int) error {
	return nil
}
func (r *ownershipRepository) AcknowledgeChatModelCleanup(context.Context, chat.TurnRef, string) error {
	return nil
}
func (r *ownershipRepository) BeginChatModelAttempt(context.Context, chat.ModelAttempt) (bool, error) {
	return r.cached, nil
}
func (r *ownershipRepository) SaveChatModelResult(context.Context, chat.ChatModelResult) error {
	return storage.ErrConflict
}
func TestChatModelCanonicalWinnerAndChangedOwnerConflict(t *testing.T) {
	ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
	t.Run("cache commits before atomic registration", func(t *testing.T) {
		a := ChatActivities{Repository: &ownershipRepository{cached: true}}
		require.NoError(t, a.ChatModel(context.Background(), ref), "no model available: must adopt cache before inference")
	})
	t.Run("different owner's canonical answer", func(t *testing.T) {
		a := ChatActivities{Repository: &ownershipRepository{winner: uuid.NewString()}, Model: mock.New()}
		require.NoError(t, a.ChatModel(context.Background(), ref))
	})
	t.Run("same owner's changed payload", func(t *testing.T) {
		a := ChatActivities{Repository: &ownershipRepository{winner: chat.LogicalID(ref.TurnID, "model/0/attempt/1")}, Model: mock.New()}
		err := a.ChatModel(context.Background(), ref)
		var app *temporal.ApplicationError
		require.ErrorAs(t, err, &app)
		require.True(t, app.NonRetryable())
		require.Equal(t, "InvalidRequest", app.Type())
	})
}
