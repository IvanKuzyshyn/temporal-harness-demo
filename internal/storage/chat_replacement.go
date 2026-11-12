package storage

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

func validChatAdmission(c chat.CommandIdentity, r chat.AdmissionResult) bool {
	if r.State == "rejected" {
		return (r.Code == "invalid_command" || r.Code == "stale_target" || r.Code == "control_conflict") && r.TargetTurnID == "" && r.ReplacementTurnID == ""
	}
	if r.State != "accepted" || r.Code != "" {
		return false
	}
	if c.Kind == chat.CommandStop || c.Kind == chat.CommandReplace {
		if r.TargetTurnID != c.ExpectedActiveTurnID || !chat.ValidID(string(r.TargetTurnID)) {
			return false
		}
		if c.Kind == chat.CommandReplace {
			return r.ReplacementTurnID == c.TurnID && chat.ValidID(string(c.TurnID))
		}
		return r.ReplacementTurnID == ""
	}
	return r.TargetTurnID == "" && r.ReplacementTurnID == ""
}

// This is a projection after the workflow's control claim and joined work, not
// SQL authorization to cancel. Idempotent across cancellation settlement retry.
func (r *Repository) MarkChatCancelling(ctx context.Context, ref chat.TurnRef) error {
	return r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE turns SET status='cancelling',updated_at=clock_timestamp() WHERE conversation_id=$1 AND id=$2 AND status IN ('queued','running','cancelling')`, ref.ConversationID, ref.TurnID)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return err
	})
}
