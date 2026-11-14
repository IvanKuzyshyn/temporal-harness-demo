//go:build integration

package storage_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWebModelStepsHaveIndependentImmutableResults(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	rc, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "first step"}))
	ref.Step = 1
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "final step"}), "a turn is not a model step")
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "completed"}))
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	require.Equal(t, "final step", page.Messages[1].Text)
	ref.Step = 0
	got, err := r.GetChatModelResult(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "first step", got.Text)
}
