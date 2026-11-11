//go:build integration

package storage_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChatFailedSettlementClosesAbandonedAttempt(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	id := uuid.NewString()
	require.NoError(t, r.RecordModelAttempt(ctx, chat.ModelAttempt{ID: id, ConversationID: c.ID, TurnID: rc.TurnID, Attempt: 2, Status: "running"}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}, Status: "failed", Reason: "model_error"}))
	var status string
	require.NoError(t, p.QueryRow(ctx, "SELECT status FROM model_steps WHERE id=$1", id).Scan(&status))
	require.Equal(t, "failed", status)
	page, err := r.ListMessages(ctx, c.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1, "no assistant completion for failed inference")
}
