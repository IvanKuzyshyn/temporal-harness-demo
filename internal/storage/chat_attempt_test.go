//go:build integration

package storage_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func beginChatAttempt(t *testing.T, r *storage.Repository, a chat.ModelAttempt) bool {
	t.Helper()
	cached, err := r.BeginChatModelAttempt(ctx, a)
	require.NoError(t, err)
	return cached
}

func TestChatResultAttemptOwnership(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	one, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	two, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	id := uuid.NewString()
	require.NoError(t, r.RecordModelAttempt(ctx, chat.ModelAttempt{ID: id, ConversationID: c.ID, TurnID: one.TurnID, Attempt: 1, Status: "running"}))
	require.Error(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: chat.TurnRef{ConversationID: c.ID, TurnID: two.TurnID}, Text: "wrong turn", AttemptID: id}))
}
func TestChatAttemptInterruptedRecovery(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	a := chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: rc.TurnID, Attempt: 1, Status: "running"}
	require.False(t, beginChatAttempt(t, r, a))
	a.ID = uuid.NewString()
	a.Attempt = 2
	require.False(t, beginChatAttempt(t, r, a))
	require.False(t, beginChatAttempt(t, r, a))
	var status, reason string
	require.NoError(t, p.QueryRow(ctx, "SELECT status,error FROM model_steps WHERE turn_id=$1 AND attempt=1", rc.TurnID).Scan(&status, &reason))
	require.Equal(t, "failed", status)
	require.Equal(t, "interrupted", reason)
}
