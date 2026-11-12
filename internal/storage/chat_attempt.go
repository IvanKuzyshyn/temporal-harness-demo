package storage

import (
	"context"
	"errors"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// ErrStaleChatAttempt means another Temporal attempt owns publication. It is not
// invalid user input. A timed-out remote activity may still be unwinding.
var ErrStaleChatAttempt = errors.New("chat model attempt superseded")

// BeginChatModelAttempt atomically checks the canonical candidate, registers the
// attempt and fences older publishers under the same conversation lock used by
// SaveChatModelResult. cached=true means reuse the winner, without changing its
// attempt projection or starting another inference.
func (r *Repository) BeginChatModelAttempt(ctx context.Context, a chat.ModelAttempt) (cached bool, err error) {
	if a.Status != "running" || !chat.ValidID(a.ID) || !chat.ValidID(string(a.TurnID)) || a.Attempt < 1 || (a.LogicalStep < 0 || a.LogicalStep >= chat.MaxWebSteps) || a.Error != "" || a.ResultEntryID != "" || len(a.Usage) != 0 {
		return false, chat.ErrInvalid
	}
	hash, err := digest(a)
	if err != nil {
		return false, err
	}
	err = r.withConversation(ctx, a.ConversationID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_model_results WHERE conversation_id=$1 AND turn_id=$2 AND logical_step=$3)`, a.ConversationID, a.TurnID, a.LogicalStep).Scan(&cached); err != nil {
			return err
		}
		if cached {
			return nil
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM turns WHERE conversation_id=$1 AND id=$2`, a.ConversationID, a.TurnID).Scan(&status); err != nil {
			return err
		}
		if status != "queued" && status != "running" {
			return ErrStaleChatAttempt
		}
		var newer bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_steps WHERE conversation_id=$1 AND turn_id=$2 AND logical_step=$4 AND attempt>$3)`, a.ConversationID, a.TurnID, a.Attempt, a.LogicalStep).Scan(&newer); err != nil {
			return err
		}
		if newer {
			return ErrStaleChatAttempt
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_steps(id,conversation_id,turn_id,logical_step,attempt,status,payload_digest) VALUES($1,$2,$3,$6,$4,'running',$5) ON CONFLICT DO NOTHING`, a.ID, a.ConversationID, a.TurnID, a.Attempt, hash, a.LogicalStep); err != nil {
			return err
		}
		var old string
		if err := tx.QueryRow(ctx, `SELECT payload_digest,status FROM model_steps WHERE id=$1 AND conversation_id=$2 AND turn_id=$3`, a.ID, a.ConversationID, a.TurnID).Scan(&old, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		if old != hash {
			return ErrConflict
		}
		if status != "running" {
			return ErrStaleChatAttempt
		}
		_, err := tx.Exec(ctx, `UPDATE model_steps SET status='failed',error='interrupted' WHERE conversation_id=$1 AND turn_id=$2 AND logical_step=$4 AND attempt<$3 AND status='running'`, a.ConversationID, a.TurnID, a.Attempt, a.LogicalStep)
		return err
	})
	return cached, err
}
