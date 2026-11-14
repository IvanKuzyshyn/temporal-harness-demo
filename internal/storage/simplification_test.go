//go:build integration

package storage_test

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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
	_, err = r.CreateConversation(ctx, "gemma4:e4b")
	require.ErrorIs(t, err, chat.ErrInvalid)
	// SQL itself must reject newly inserted disabled models, not just Go callers.
	_, err = p.Exec(ctx, "INSERT INTO conversations(id,model,workflow_id) VALUES($1,'gemma4:e4b',$2)", uuid.NewString(), uuid.NewString())
	require.Error(t, err)
}

// Both joins and their message share a transaction, so the existing immutable
// message trigger cannot mask a missing max-one constraint. Negative control
// drops ONLY that constraint, in a rolled-back transaction on the test DB.
func TestAttachmentSQLSingleBoundary(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	ids := []chat.AttachmentID{}
	for i := 0; i < 2; i++ {
		a := attachment(c.ID)
		require.NoError(t, r.CreateAttachment(ctx, a))
		require.NoError(t, r.MarkAttachmentReady(ctx, c.ID, a.ID, a.Checksum))
		ids = append(ids, a.ID)
	}
	for _, enforce := range []bool{true, false} {
		name := "constraint_present"
		if !enforce {
			name = "only_new_constraint_removed"
		}
		t.Run(name, func(t *testing.T) {
			tx, err := p.Begin(ctx)
			require.NoError(t, err)
			defer tx.Rollback(ctx)
			if !enforce {
				_, err = tx.Exec(ctx, "ALTER TABLE message_attachments DROP CONSTRAINT single_message_attachment")
				require.NoError(t, err)
			}
			turn, entry := uuid.NewString(), uuid.NewString()
			_, err = tx.Exec(ctx, "INSERT INTO turns(id,conversation_id,request_id,kind) VALUES($1,$2,$3,'message')", turn, c.ID, uuid.NewString())
			require.NoError(t, err)
			_, err = tx.Exec(ctx, `INSERT INTO messages(entry_id,conversation_id,turn_id,role,text,logical_step,result_kind,completion_state,payload_digest) VALUES($1,$2,$3,'user','two ready files',0,'prompt','completed',$4)`, entry, c.ID, turn, strings.Repeat("b", 64))
			require.NoError(t, err)
			_, err = tx.Exec(ctx, "INSERT INTO message_attachments(conversation_id,entry_id,attachment_id,ordinal) VALUES($1,$2,$3,0)", c.ID, entry, ids[0])
			require.NoError(t, err, "valid first attachment must pass all existing fences")
			_, err = tx.Exec(ctx, "INSERT INTO message_attachments(conversation_id,entry_id,attachment_id,ordinal) VALUES($1,$2,$3,1)", c.ID, entry, ids[1])
			if enforce {
				var constraint *pgconn.PgError
				require.ErrorAs(t, err, &constraint)
				require.Equal(t, "23514", constraint.Code)
				require.Equal(t, "single_message_attachment", constraint.ConstraintName)
			} else {
				require.NoError(t, err, "negative control must allow both otherwise-valid joins")
				var count int
				require.NoError(t, tx.QueryRow(ctx, "SELECT count(*) FROM message_attachments WHERE entry_id=$1", entry).Scan(&count))
				require.Equal(t, 2, count)
			}
		})
	}
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
