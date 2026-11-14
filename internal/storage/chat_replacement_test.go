//go:build integration

package storage_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplacementPromptAndAttachmentProvenance(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	file := attachment(c.ID)
	require.NoError(t, r.CreateAttachment(ctx, file))
	require.NoError(t, r.MarkAttachmentReady(ctx, c.ID, file.ID, file.Checksum))
	original := input(c.ID)
	original.Payload.Text = "original text"
	original.Payload.AttachmentIDs = []chat.AttachmentID{file.ID}
	first, err := r.AcceptCommand(ctx, original)
	require.NoError(t, err)
	correction := input(c.ID)
	correction.Kind = chat.CommandReplace
	correction.Payload.ExpectedActiveTurnID = first.TurnID
	correction.Payload.Text = "correction"
	next, err := r.AcceptCommand(ctx, correction)
	require.NoError(t, err)
	turn, err := r.GetTurn(ctx, chat.TurnRef{ConversationID: c.ID, TurnID: next.TurnID})
	require.NoError(t, err)
	require.Equal(t, "correction", turn.Text)
	require.Equal(t, "original text", turn.OriginalText)
	require.Equal(t, first.TurnID, turn.ReplacementTarget)
	require.Equal(t, []chat.AttachmentID{file.ID}, turn.OriginalAttachmentIDs)
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	require.Equal(t, "original text", page.Messages[0].Text)
	require.Equal(t, []chat.AttachmentID{file.ID}, page.Messages[0].AttachmentIDs)
}

// Cancelling a cached response must not publish it as completed context.
func TestCancelledDraftExcludedFromContext(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	first, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: first.TurnID}
	require.NoError(t, r.StartChatTurn(ctx, ref))
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Text: "cancelled draft"}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "cancelled", Reason: "replaced"}))
	require.NoError(t, r.PersistTurnResult(ctx, chat.TurnResult{TurnRef: ref, Status: "cancelled", Reason: "replaced"}))
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	require.Equal(t, "incomplete", page.Messages[1].CompletionState)
	require.Equal(t, "cancelled", page.Messages[1].ResultKind)
	next := input(c.ID)
	next.RequestID = uuid.NewString()
	rc, err := r.AcceptCommand(ctx, next)
	require.NoError(t, err)
	history, err := r.RecentChatExchanges(ctx, chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID})
	require.NoError(t, err)
	require.Empty(t, history)
}
