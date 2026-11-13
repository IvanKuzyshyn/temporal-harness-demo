package storage

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// EventSnapshot reads content, status and cursor in ONE MVCC snapshot. No HTTP
// writer holds this transaction: slow readers cannot pin a lock or worker.
// Lifecycle reconciliation does not depend on retained display events.
type EventSnapshot struct {
	Projection                       ConversationProjection
	History                          chat.MessagePage
	Cursor                           int64
	Turns, Commands, Tools, Attempts json.RawMessage
}

func (r *Repository) EventSnapshot(ctx context.Context, id chat.ConversationID) (out EventSnapshot, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	out.Projection, err = conversationProjection(ctx, tx, id)
	if err != nil {
		return out, err
	}
	out.History, err = listMessages(ctx, tx, id, "", 20)
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, "SELECT event_sequence FROM conversations WHERE id=$1", id).Scan(&out.Cursor)
	if err != nil {
		return out, err
	}
	// Bounded recent detail windows. Older messages remain independently pageable;
	// pending command receipts retained by the client can always be read by ID.
	for _, q := range []struct {
		sql string
		dst *json.RawMessage
	}{
		{`SELECT coalesce(jsonb_agg(x),'[]') FROM (SELECT id::text, status, reason FROM turns WHERE conversation_id=$1 ORDER BY updated_at DESC,id LIMIT 200) x`, &out.Turns},
		{`SELECT coalesce(jsonb_agg(x),'[]') FROM (SELECT id::text AS "commandId",request_id::text AS "requestId",turn_id::text AS "turnId",payload_digest AS "payloadDigest",dispatch_state AS "dispatchState",result_state AS "resultState",result FROM commands WHERE conversation_id=$1 ORDER BY sequence DESC LIMIT 200) x`, &out.Commands},
		{`SELECT coalesce(jsonb_agg(x),'[]') FROM (SELECT id::text,turn_id::text AS "turnId",name,status,result,sources FROM tool_calls WHERE conversation_id=$1 ORDER BY created_at DESC,id LIMIT 32) x`, &out.Tools},
		{`SELECT coalesce(jsonb_agg(x),'[]') FROM (SELECT "turnId","stepId",attempt,status FROM (SELECT DISTINCT ON (turn_id,logical_step) turn_id::text AS "turnId",logical_step AS "stepId",attempt,status,created_at FROM model_steps WHERE conversation_id=$1 ORDER BY turn_id,logical_step,attempt DESC) recent ORDER BY created_at DESC LIMIT 200) x`, &out.Attempts},
	} {
		if err = tx.QueryRow(ctx, q.sql, id).Scan(q.dst); err != nil {
			return out, err
		}
	}
	return out, tx.Commit(ctx)
}

// Resume is only valid for a retained cursor in this conversation and a bounded
// replay window. Future/missing/expired cursors reset; no arithmetic gap guesses.
func (r *Repository) CanResumeEvents(ctx context.Context, id chat.ConversationID, after int64) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE conversation_id=$1 AND sequence=$2) AND (SELECT count(*) FROM (SELECT 1 FROM events WHERE conversation_id=$1 AND sequence>$2 LIMIT 257) recent)<=256`, id, after).Scan(&ok)
	return ok, err
}
