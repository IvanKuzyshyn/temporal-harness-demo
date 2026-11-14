package storage

import (
	"context"
	"encoding/json"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// ResolveClosedChatCommand requires a confirmed terminal workflow and its
// replayed inbox/active-turn observation. Receipt absence is NOT non-admission.
// Serialize the resolution with late admission writers so the returned result
// cannot subsequently disagree with execution_receipt. This never settles a turn.
func (r *Repository) ResolveClosedChatCommand(ctx context.Context, c chat.SessionCommand, enqueued bool, observed ...chat.AdmissionResult) (out chat.AdmissionResult, err error) {
	if _, err = r.CheckChatCommand(ctx, c); err != nil {
		return out, err
	}
	if len(observed) > 1 || (len(observed) == 1 && !validChatAdmission(c.Identity(), observed[0])) {
		return out, chat.ErrInvalid
	}
	err = r.withConversation(ctx, c.ConversationID, func(tx pgx.Tx) error {
		var raw []byte
		var status, reason string
		if err := tx.QueryRow(ctx, `SELECT c.execution_receipt,coalesce(t.status,''),coalesce(t.reason,'') FROM commands c LEFT JOIN turns t ON t.id=c.turn_id AND t.conversation_id=c.conversation_id WHERE c.id=$1 AND c.conversation_id=$2`, c.CommandID, c.ConversationID).Scan(&raw, &status, &reason); err != nil {
			return err
		}
		if len(raw) != 0 {
			return json.Unmarshal(raw, &out)
		}
		out = chat.AdmissionResult{State: "rejected", Code: "conversation_closed"}
		if enqueued || status == "running" || status == "completed" || status == "cancelled" || (status == "failed" && reason != "invalid_command" && reason != "stale_target" && reason != "control_conflict") {
			out = chat.AdmissionResult{State: "accepted"}
			if c.Kind == chat.CommandReplace {
				out.TargetTurnID = c.Payload.ExpectedActiveTurnID
				out.ReplacementTurnID = c.TurnID
			}
		}
		if len(observed) == 1 {
			out = observed[0]
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE commands SET execution_receipt=$2 WHERE id=$1`, c.CommandID, raw)
		return err
	})
	return out, err
}
