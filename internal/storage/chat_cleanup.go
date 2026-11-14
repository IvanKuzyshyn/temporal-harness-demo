package storage

import (
	"context"
	"errors"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

var ErrChatCleanupUnconfirmed = errors.New("chat model cleanup unconfirmed")

// RegisterChatModelInvocation must commit BEFORE any provider work can start.
// A unique invocation ID (not the logical attempt ID) also covers redelivery.
// Registration and sealing share the conversation lock: a late registration is
// either counted by the seal or rejected, never invisible to the barrier.
func (r *Repository) RegisterChatModelInvocation(ctx context.Context, ref chat.TurnRef, id string, attempt int) error {
	if !chat.ValidID(id) || !chat.ValidID(string(ref.TurnID)) || attempt < 1 {
		return chat.ErrInvalid
	}
	return r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT NOT model_invocations_closed AND status IN ('queued','running') FROM turns WHERE conversation_id=$1 AND id=$2`, ref.ConversationID, ref.TurnID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return ErrStaleChatAttempt
		}
		// Deliberately not an idempotent reauthorization: a repeated invocation ID
		// must never start a second body after its cleanup has been acknowledged.
		_, err := tx.Exec(ctx, `INSERT INTO chat_model_invocations(id,conversation_id,turn_id,attempt) VALUES($1,$2,$3,$4)`, id, ref.ConversationID, ref.TurnID, attempt)
		return err
	})
}

// AcknowledgeChatModelCleanup is called by the registered body only, AFTER all
// its provider/persistence work and heartbeat/shutdown goroutines have joined.
// A failed/missing ack is uncertainty, never inferred from model_steps status.
func (r *Repository) AcknowledgeChatModelCleanup(ctx context.Context, ref chat.TurnRef, id string) error {
	return r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE chat_model_invocations SET cleanup_confirmed_at=coalesce(cleanup_confirmed_at,clock_timestamp()) WHERE conversation_id=$1 AND turn_id=$2 AND id=$3`, ref.ConversationID, ref.TurnID, id)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return err
	})
}

// SealChatModelInvocations closes admission permanently and returns the exact
// number of outstanding acknowledgments. Zero is stable: no body can register
// afterwards. Nonzero is durably inspectable, even across worker/server restart.
func (r *Repository) SealChatModelInvocations(ctx context.Context, ref chat.TurnRef) (pending int64, err error) {
	err = r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE turns SET model_invocations_closed=true WHERE conversation_id=$1 AND id=$2`, ref.ConversationID, ref.TurnID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM chat_model_invocations WHERE conversation_id=$1 AND turn_id=$2 AND cleanup_confirmed_at IS NULL`, ref.ConversationID, ref.TurnID).Scan(&pending)
	})
	return
}
