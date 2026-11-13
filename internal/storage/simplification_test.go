//go:build integration

package storage_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestAttachmentSingleBoundary(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	ids := []chat.AttachmentID{}
	for i := 0; i < 2; i++ {
		a := chat.Attachment{ID: chat.AttachmentID(uuid.NewString()), ConversationID: c.ID, OriginalName: "a.txt", MediaType: "text/plain", Size: 1, Checksum: strings.Repeat("a", 64), StorageKey: uuid.NewString(), Status: "staged"}
		require.NoError(t, r.CreateAttachment(ctx, a))
		require.NoError(t, r.MarkAttachmentReady(ctx, c.ID, a.ID, a.Checksum))
		ids = append(ids, a.ID)
	}
	for _, kind := range []chat.CommandKind{chat.CommandMessage, chat.CommandReplace} {
		in := input(c.ID)
		in.Kind = kind
		in.Payload.AttachmentIDs = ids
		if kind == chat.CommandReplace {
			in.Payload.ExpectedActiveTurnID = chat.TurnID(uuid.NewString())
		}
		_, err := r.AcceptCommand(ctx, in)
		require.ErrorIs(t, err, chat.ErrInvalid)
	}
	require.Zero(t, count(t, p, "messages", c.ID))
	require.Zero(t, count(t, p, "commands", c.ID))
	in := input(c.ID)
	in.Payload.AttachmentIDs = ids[:1]
	receipt, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	again, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	require.Equal(t, receipt, again)
	m := assistant(c.ID, receipt.TurnID)
	m.AttachmentIDs = ids
	require.ErrorIs(t, r.AppendMessage(ctx, m), chat.ErrInvalid)
	require.Equal(t, 1, count(t, p, "messages", c.ID))
	_, err = p.Exec(ctx, `INSERT INTO message_attachments(conversation_id,entry_id,attachment_id,ordinal) SELECT conversation_id,entry_id,$2,1 FROM messages WHERE conversation_id=$1`, c.ID, ids[1])
	require.Error(t, err, "SQL must independently reject a second attachment")
	_, err = r.CreateConversation(ctx, "gemma4:e4b")
	require.ErrorIs(t, err, chat.ErrInvalid)
	// SQL itself must reject newly inserted disabled models, not just Go callers.
	_, err = p.Exec(ctx, "INSERT INTO conversations(id,model,workflow_id) VALUES($1,'gemma4:e4b',$2)", uuid.NewString(), uuid.NewString())
	require.Error(t, err)
}

func TestDisabledStoredModelPreservedButNotSubstituted(t *testing.T) {
	r, p := database(t)
	// Atomic test-only simulation of a pre-migration row. NOT VALID must retain it.
	id := chat.ConversationID(uuid.NewString())
	tx, err := p.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "ALTER TABLE conversations DROP CONSTRAINT enabled_chat_model")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "INSERT INTO conversations(id,model,workflow_id) VALUES($1,'gemma4:e4b',$2)", id, "web-chat/"+string(id))
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "ALTER TABLE conversations ADD CONSTRAINT enabled_chat_model CHECK (model IN ('qwen3.5:9b')) NOT VALID")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	c, err := r.GetConversation(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "gemma4:e4b", c.Model)
	for _, kind := range []chat.CommandKind{chat.CommandMessage, chat.CommandReplace} {
		in := input(id)
		in.Kind = kind
		if kind == chat.CommandReplace {
			in.Payload.ExpectedActiveTurnID = chat.TurnID(uuid.NewString())
		}
		_, err = r.AcceptCommand(ctx, in)
		require.ErrorIs(t, err, chat.ErrInvalid)
	}
	require.Zero(t, count(t, p, "messages", id))
	require.Zero(t, count(t, p, "commands", id))
}
