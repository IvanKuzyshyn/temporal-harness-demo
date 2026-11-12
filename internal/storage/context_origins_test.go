//go:build integration

package storage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestContextQuotationScopeCompletionOrderAndBound(t *testing.T) {
	r, p := database(t)
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	accept := func(n int) chat.TurnRef {
		in := input(c.ID)
		in.Payload.Text = fmt.Sprintf("User source %d.", n)
		cmd, e := r.AcceptCommand(ctx, in)
		require.NoError(t, e)
		return chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID}
	}
	complete := func(ref chat.TurnRef) {
		require.NoError(t, r.StartChatTurn(ctx, ref))
		require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "Assistant declined the request."}))
		require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	}
	first, second := accept(0), accept(1)
	complete(second)
	complete(first)
	for n := 2; n < 12; n++ {
		complete(accept(n))
	}
	cross, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	in := input(cross.ID)
	in.Payload.Text = "cross conversation source"
	cmd, e := r.AcceptCommand(ctx, in)
	require.NoError(t, e)
	complete(chat.TurnRef{ConversationID: cross.ID, TurnID: cmd.TurnID})
	current := accept(12)
	state, e := r.LoadContext(ctx, current)
	require.NoError(t, e)
	require.Equal(t, 6, state.QuotationTotal)
	require.Len(t, state.Quotations, 4)
	require.Equal(t, second.TurnID, state.Quotations[0].User.TurnID, "completion, not admission order")
	require.Equal(t, first.TurnID, state.Quotations[1].User.TurnID)
	for i, pair := range state.Quotations {
		require.Equal(t, "user", pair.User.Speaker)
		require.Equal(t, "assistant", pair.Assistant.Speaker)
		require.Equal(t, pair.User.TurnID, pair.Assistant.TurnID)
		require.Equal(t, pair.User.Sequence, pair.Assistant.Sequence)
		require.True(t, chat.ValidID(pair.User.SourceID))
		require.True(t, chat.ValidID(pair.Assistant.SourceID))
		require.Equal(t, len(pair.User.Text), pair.User.Bytes)
		hash := sha256.Sum256([]byte(pair.User.Text))
		require.Equal(t, hex.EncodeToString(hash[:]), pair.User.TextHash)
		require.Equal(t, "Assistant declined the request.", pair.Assistant.Text)
		require.NotEqual(t, "cross conversation source", pair.User.Text)
		var id string
		require.NoError(t, p.QueryRow(ctx, "SELECT entry_id::text FROM messages WHERE turn_id=$1 AND role='user'", pair.User.TurnID).Scan(&id))
		require.Equal(t, id, pair.User.SourceID)
		if i > 0 {
			require.Greater(t, pair.User.Sequence, state.Quotations[i-1].User.Sequence)
		}
		for _, recent := range state.Recent {
			require.NotEqual(t, recent.Sequence, pair.User.Sequence, "no duplicate recent pair")
		}
	}
	again, e := storage.New(p).LoadContext(ctx, current)
	require.NoError(t, e)
	require.Equal(t, state.Quotations, again.Quotations)
	// Full original SQL transcript is retained, not replaced by quotations/summary.
	page, e := r.ListMessages(ctx, c.ID, "", 100)
	require.NoError(t, e)
	require.Len(t, page.Messages, 25)
}
