package storage

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// BeginChatModelAttempt records the new attempt and reconciles abandoned older
// projections. Temporal's attempt number is authority; this SQL never schedules
// inference. If either write loses its acknowledgement, repeating is harmless.
func (r *Repository) BeginChatModelAttempt(ctx context.Context, a chat.ModelAttempt) error {
	if a.Status != "running" {
		return chat.ErrInvalid
	}
	if err := r.RecordModelAttempt(ctx, a); err != nil {
		return err
	}
	return r.withConversation(ctx, a.ConversationID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE model_steps SET status='failed',error='interrupted' WHERE conversation_id=$1 AND turn_id=$2 AND logical_step=$3 AND attempt<$4 AND status='running'`, a.ConversationID, a.TurnID, a.LogicalStep, a.Attempt)
		return err
	})
}
