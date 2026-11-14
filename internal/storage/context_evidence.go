package storage

import (
	"context"
	"encoding/json"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

const recordedContextEvidence = ` FROM tool_calls c JOIN turns t ON t.id=c.turn_id
 JOIN messages a ON a.turn_id=t.id AND a.role='assistant' AND a.result_kind='final' AND a.completion_state='completed'
 WHERE c.conversation_id=$1 AND t.conversation_id=$1 AND t.id<>$2 AND t.status='completed'
 AND c.status IN ('completed','failed') AND c.authorized_scope IS NOT NULL AND c.result_owner IS NOT NULL`

// One deterministic bounded selection independent of generative summary coverage.
// No attachment table, original store, filename lookup or fresh authorization.
func loadContextEvidence(ctx context.Context, tx pgx.Tx, ref chat.TurnRef, out *chat.ContextHistory) error {
	if e := tx.QueryRow(ctx, `SELECT count(*)`+recordedContextEvidence, ref.ConversationID, ref.TurnID).Scan(&out.EvidenceTotal); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT a.sequence,c.authorized_scope,c.arguments,c.result`+recordedContextEvidence+` ORDER BY a.sequence DESC,c.id LIMIT $3`, ref.ConversationID, ref.TurnID, chat.MaxContextEvidence)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var item chat.ContextEvidence
		var scope, result []byte
		if e = rows.Scan(&item.Source.Sequence, &scope, &item.Arguments, &result); e != nil {
			return e
		}
		if e = json.Unmarshal(scope, &item.Source.Scope); e != nil {
			return e
		}
		if e = json.Unmarshal(result, &item.Result); e != nil {
			return e
		}
		item.Arguments, e = canonicalJSON(item.Arguments)
		if e != nil {
			return e
		}
		item.Source.ArgumentsHash, e = digest(item.Arguments)
		if e != nil {
			return e
		}
		item.Source.ResultHash, e = digest(item.Result)
		if e != nil {
			return e
		}
		item.Source.AttachmentID = item.Result.AttachmentID
		if item.Source.AttachmentID == "" {
			var args struct {
				AttachmentID chat.AttachmentID `json:"attachmentId"`
			}
			if e = json.Unmarshal(item.Arguments, &args); e != nil {
				return e
			}
			item.Source.AttachmentID = args.AttachmentID
		}
		out.Evidence = append(out.Evidence, item)
	}
	return rows.Err()
}
