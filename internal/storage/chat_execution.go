package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// CheckChatCommand verifies ALL persisted identities. A receipt is safe to reuse
// only under the enqueue-before-activity protocol in WebChatSessionWorkflow.
func (r *Repository) CheckChatCommand(ctx context.Context, c chat.SessionCommand) (chat.AdmissionResult, error) {
	var out chat.AdmissionResult
	hash, err := (chat.CommandInput{ConversationID: c.ConversationID, RequestID: c.RequestID, Kind: c.Kind, Payload: c.Payload}).Digest()
	if err != nil || hash != c.PayloadDigest || !chat.ValidID(string(c.CommandID)) {
		return out, ErrConflict
	}
	var raw []byte
	err = r.pool.QueryRow(ctx, `SELECT c.execution_receipt FROM commands c JOIN conversations v ON v.id=c.conversation_id
 WHERE c.id=$1 AND c.conversation_id=$2 AND c.request_id=$3 AND coalesce(c.turn_id::text,'')=$4 AND c.payload_digest=$5 AND c.kind=$6 AND c.sequence=$7 AND v.workflow_id=$8 AND v.model=$9`, c.CommandID, c.ConversationID, c.RequestID, c.TurnID, c.PayloadDigest, c.Kind, c.Sequence, c.WorkflowID, c.Model).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrConflict
	}
	if err != nil {
		return out, err
	}
	if raw != nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (r *Repository) RecordChatAdmission(ctx context.Context, a chat.Admission) error {
	if a.Result.State != "accepted" && !(a.Result.State == "rejected" && a.Result.Code == "invalid_command") {
		return chat.ErrInvalid
	}
	if _, err := r.CheckChatCommand(ctx, a.Command); err != nil {
		return err
	}
	raw, _ := json.Marshal(a.Result)
	return r.withConversation(ctx, a.Command.ConversationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE commands SET execution_receipt=$2 WHERE id=$1 AND (execution_receipt IS NULL OR execution_receipt=$2::jsonb)`, a.Command.CommandID, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		// An unsupported replacement/attachment still has a queued intent turn.
		if a.Result.State == "rejected" && a.Command.TurnID != "" {
			_, err = tx.Exec(ctx, `UPDATE turns SET status='failed',reason='invalid_command',updated_at=clock_timestamp() WHERE id=$1 AND status='queued'`, a.Command.TurnID)
		}
		return err
	})
}
func (r *Repository) GetTurn(ctx context.Context, ref chat.TurnRef) (chat.Turn, error) {
	out := chat.Turn{TurnRef: ref}
	if !chat.ValidID(string(ref.ConversationID)) || !chat.ValidID(string(ref.TurnID)) {
		return out, chat.ErrInvalid
	}
	err := r.pool.QueryRow(ctx, `SELECT t.status,t.reason,m.text,m.sequence,v.model FROM turns t JOIN conversations v ON v.id=t.conversation_id JOIN messages m ON m.turn_id=t.id AND m.result_kind='prompt' AND m.role='user' WHERE t.conversation_id=$1 AND t.id=$2`, ref.ConversationID, ref.TurnID).Scan(&out.Status, &out.Reason, &out.Text, &out.Sequence, &out.Model)
	return out, dbError(err)
}
func (r *Repository) StartChatTurn(ctx context.Context, ref chat.TurnRef) error {
	return r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE turns SET status='running',updated_at=clock_timestamp() WHERE conversation_id=$1 AND id=$2 AND status IN ('queued','running')`, ref.ConversationID, ref.TurnID)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return err
	})
}
func (r *Repository) GetChatModelResult(ctx context.Context, ref chat.TurnRef) (chat.ChatModelResult, error) {
	out := chat.ChatModelResult{TurnRef: ref}
	err := r.pool.QueryRow(ctx, `SELECT text,tokens,coalesce(attempt_id::text,''),limited_history FROM chat_model_results WHERE conversation_id=$1 AND turn_id=$2`, ref.ConversationID, ref.TurnID).Scan(&out.Text, &out.Tokens, &out.AttemptID, &out.LimitedHistory)
	return out, dbError(err)
}
func (r *Repository) SaveChatModelResult(ctx context.Context, a chat.ChatModelResult) error {
	if !chat.ValidID(string(a.TurnID)) || len(a.Text) > 65536 || a.Tokens < 0 {
		return chat.ErrInvalid
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.withConversation(ctx, a.ConversationID, func(tx pgx.Tx) error {
		var old string
		err := tx.QueryRow(ctx, `SELECT payload_digest FROM chat_model_results WHERE turn_id=$1`, a.TurnID).Scan(&old)
		if err == nil {
			if old != hash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_model_results(conversation_id,turn_id,text,tokens,attempt_id,limited_history,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.ConversationID, a.TurnID, a.Text, a.Tokens, nullable(a.AttemptID), a.LimitedHistory, hash)
		return err
	})
}

// RecentChatExchanges preserves whole text exchanges, never queued prompts,
// failed drafts, documents or attachment content. The caller applies a byte budget.
func (r *Repository) RecentChatExchanges(ctx context.Context, ref chat.TurnRef) ([]chat.Exchange, error) {
	rows, err := r.pool.Query(ctx, `SELECT u.text,a.text,u.sequence FROM turns t
 JOIN messages u ON u.turn_id=t.id AND u.role='user' AND u.result_kind='prompt'
 JOIN messages a ON a.turn_id=t.id AND a.role='assistant' AND a.result_kind='final'
 WHERE t.conversation_id=$1 AND t.status='completed' AND u.sequence<(SELECT sequence FROM messages WHERE turn_id=$2 AND result_kind='prompt')
 ORDER BY u.sequence DESC LIMIT 6`, ref.ConversationID, ref.TurnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.Exchange
	for rows.Next() {
		var e chat.Exchange
		if err = rows.Scan(&e.User, &e.Assistant, &e.Sequence); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PersistTurnResult commits final message + terminal projection + durable event
// in ONE transaction. Stable logical IDs and a settlement digest fence changed
// outcomes. Cached provider output alone is never an assistant completion.
func (r *Repository) PersistTurnResult(ctx context.Context, a chat.TurnResult) error {
	if !chat.ValidID(string(a.TurnID)) || (a.Status != "completed" && a.Status != "failed") || len(a.Reason) > 128 || (a.Status == "completed" && a.Reason != "") {
		return chat.ErrInvalid
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.withConversation(ctx, a.ConversationID, func(tx pgx.Tx) error {
		var old *string
		if err := tx.QueryRow(ctx, `SELECT settlement_digest FROM turns WHERE conversation_id=$1 AND id=$2 FOR UPDATE`, a.ConversationID, a.TurnID).Scan(&old); err != nil {
			return err
		}
		if old != nil {
			if *old != hash {
				return ErrConflict
			}
			return nil
		}
		entry := chat.LogicalID(a.TurnID, "final")
		limited := false
		if a.Status == "completed" {
			var text, attempt string
			var tokens int
			err := tx.QueryRow(ctx, `SELECT text,tokens,coalesce(attempt_id::text,''),limited_history FROM chat_model_results WHERE conversation_id=$1 AND turn_id=$2`, a.ConversationID, a.TurnID).Scan(&text, &tokens, &attempt, &limited)
			if err != nil {
				return err
			}
			if err = appendMessage(ctx, tx, chat.MessageRecord{EntryID: entry, ConversationID: a.ConversationID, TurnID: a.TurnID, Role: "assistant", Text: text, ResultKind: "final", CompletionState: "completed"}); err != nil {
				return err
			}
			if attempt != "" {
				usage, _ := json.Marshal(map[string]int{"tokens": tokens})
				if _, err = tx.Exec(ctx, `UPDATE model_steps SET status='completed',result_entry_id=$2,usage=$3 WHERE id=$1 AND turn_id=$4 AND status='running'`, attempt, entry, usage, a.TurnID); err != nil {
					return err
				}
			}
		}
		// Heartbeat/time-out exhaustion may leave the final attempt unable to
		// write its own failure. The workflow's terminal settlement closes those
		// projections without claiming a provider response or authorizing work.
		if a.Status == "failed" {
			if _, err := tx.Exec(ctx, `UPDATE model_steps SET status='failed',error=$3 WHERE conversation_id=$1 AND turn_id=$2 AND status='running'`, a.ConversationID, a.TurnID, a.Reason); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE turns SET status=$3,reason=$4,settlement_digest=$5,updated_at=clock_timestamp() WHERE conversation_id=$1 AND id=$2`, a.ConversationID, a.TurnID, a.Status, a.Reason, hash); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"status": a.Status, "reason": a.Reason, "limitedHistory": limited})
		_, err := tx.Exec(ctx, `INSERT INTO events(id,conversation_id,turn_id,type,payload,payload_digest) VALUES($1,$2,$3,$4,$5,$6)`, chat.LogicalID(a.TurnID, "settlement"), a.ConversationID, a.TurnID, "turn."+a.Status, payload, hash)
		return err
	})
}
