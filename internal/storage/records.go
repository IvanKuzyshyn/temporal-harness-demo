package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

// insertRecord implements immutable, retry-safe snapshot writes. Each caller
// supplies a fixed SQL statement (never caller-provided table/column names).
// A unique logical-key collision with another entry ID is a conflict, not a
// second completion. Every write locks the owning conversation first.
func (r *Repository) insertRecord(ctx context.Context, c chat.ConversationID, hash string, query string, args []any, lookup string, key string) error {
	return r.withConversation(ctx, c, func(tx pgx.Tx) error { return insertRecord(ctx, tx, hash, query, args, lookup, key) })
}
func insertRecord(ctx context.Context, tx pgx.Tx, hash, query string, args []any, lookup, key string) error {
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var existing string
	if err = tx.QueryRow(ctx, lookup, key).Scan(&existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if hash != existing {
		return ErrConflict
	}
	return nil
}

// CreateAttachment registers metadata only. Status must be staged; the caller
// finalizes bytes durably before MarkAttachmentReady. No extraction occurs here.
func (r *Repository) CreateAttachment(ctx context.Context, a chat.Attachment) error {
	if !chat.ValidID(string(a.ID)) || a.Status != "staged" || a.ExtractedText != "" || len(a.DerivedImageKeys) != 0 || !chat.ValidHash(a.Checksum) {
		return chat.ErrInvalid
	}
	a.DerivedImageKeys = nil
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.insertRecord(ctx, a.ConversationID, hash, `INSERT INTO attachments(id,conversation_id,original_name,media_type,size,checksum,storage_key,status,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, []any{a.ID, a.ConversationID, a.OriginalName, a.MediaType, a.Size, a.Checksum, a.StorageKey, a.Status, hash}, "SELECT payload_digest FROM attachments WHERE id=$1", string(a.ID))
}
func (r *Repository) GetAttachment(ctx context.Context, c chat.ConversationID, id chat.AttachmentID) (chat.Attachment, error) {
	var a chat.Attachment
	var images []byte
	if !chat.ValidID(string(c)) || !chat.ValidID(string(id)) {
		return a, chat.ErrInvalid
	}
	err := r.pool.QueryRow(ctx, "SELECT id::text,conversation_id::text,original_name,media_type,size,checksum,storage_key,status,extracted_text,derived_image_keys FROM attachments WHERE conversation_id=$1 AND id=$2", c, id).Scan(&a.ID, &a.ConversationID, &a.OriginalName, &a.MediaType, &a.Size, &a.Checksum, &a.StorageKey, &a.Status, &a.ExtractedText, &images)
	if err != nil {
		return a, dbError(err)
	}
	err = json.Unmarshal(images, &a.DerivedImageKeys)
	return a, err
}
func (r *Repository) MarkAttachmentReady(ctx context.Context, c chat.ConversationID, id chat.AttachmentID, checksum string) error {
	if !chat.ValidID(string(id)) || !chat.ValidHash(checksum) {
		return chat.ErrInvalid
	}
	return r.withConversation(ctx, c, func(tx pgx.Tx) error {
		var actual, status string
		if err := tx.QueryRow(ctx, "SELECT checksum,status FROM attachments WHERE conversation_id=$1 AND id=$2 FOR UPDATE", c, id).Scan(&actual, &status); err != nil {
			return err
		}
		if actual != checksum || status == "failed" {
			return ErrConflict
		}
		_, err := tx.Exec(ctx, "UPDATE attachments SET status='ready' WHERE conversation_id=$1 AND id=$2", c, id)
		return err
	})
}

// RecordModelAttempt registers an attempt or its settled snapshot. A running
// attempt can be settled with CompleteModelAttempt; creation retries retain the
// original digest. Live drafts belong to events and do not duplicate messages.
func (r *Repository) RecordModelAttempt(ctx context.Context, a chat.ModelAttempt) error {
	if !chat.ValidID(a.ID) || !chat.ValidID(string(a.TurnID)) || a.Attempt <= 0 || a.LogicalStep < 0 || (a.ResultEntryID != "" && !chat.ValidID(a.ResultEntryID)) {
		return chat.ErrInvalid
	}
	var err error
	a.Usage, err = canonicalJSON(a.Usage)
	if err != nil {
		return err
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.insertRecord(ctx, a.ConversationID, hash, `INSERT INTO model_steps(id,conversation_id,turn_id,logical_step,attempt,status,usage,result_entry_id,error,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, []any{a.ID, a.ConversationID, a.TurnID, a.LogicalStep, a.Attempt, a.Status, []byte(a.Usage), nullable(a.ResultEntryID), a.Error, hash}, "SELECT payload_digest FROM model_steps WHERE id=$1", a.ID)
}

// RecordToolCall stores an immutable action/result snapshot, not authorization.
func (r *Repository) RecordToolCall(ctx context.Context, a chat.ToolCall) error {
	if !chat.ValidID(a.ID) || !chat.ValidID(string(a.TurnID)) || !chat.ValidHash(a.ActionHash) {
		return chat.ErrInvalid
	}
	var err error
	a.Arguments, err = canonicalJSON(a.Arguments)
	if err != nil {
		return err
	}
	a.Result, err = canonicalJSON(a.Result)
	if err != nil {
		return err
	}
	if len(a.Sources) == 0 {
		a.Sources = []byte(`[]`)
	}
	a.Sources, err = canonicalJSON(a.Sources)
	if err != nil {
		return err
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.insertRecord(ctx, a.ConversationID, hash, `INSERT INTO tool_calls(id,conversation_id,turn_id,logical_key,name,arguments,action_hash,parent_workflow_id,child_workflow_id,status,result,sources,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, []any{a.ID, a.ConversationID, a.TurnID, a.LogicalKey, a.Name, []byte(a.Arguments), a.ActionHash, a.ParentWorkflowID, nullable(a.ChildWorkflowID), a.Status, []byte(a.Result), []byte(a.Sources), hash}, "SELECT payload_digest FROM tool_calls WHERE id=$1", a.ID)
}

// RecordApproval stores a workflow projection, not an approval policy decision.
func (r *Repository) RecordApproval(ctx context.Context, a chat.Approval) error {
	if !chat.ValidID(a.ID) || !chat.ValidID(a.ToolCallID) || !chat.ValidHash(a.ActionHash) || a.Deadline.IsZero() {
		return chat.ErrInvalid
	}
	a.Deadline = a.Deadline.UTC()
	if a.DecidedAt != nil {
		t := a.DecidedAt.UTC()
		a.DecidedAt = &t
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.insertRecord(ctx, a.ConversationID, hash, `INSERT INTO approvals(id,conversation_id,tool_call_id,action_hash,deadline,decision,policy_revision,decided_at,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, []any{a.ID, a.ConversationID, a.ToolCallID, a.ActionHash, a.Deadline, nullable(string(a.Decision)), a.PolicyRevision, a.DecidedAt, hash}, "SELECT payload_digest FROM approvals WHERE id=$1", a.ID)
}
func (r *Repository) SaveSummary(ctx context.Context, a chat.Summary) error {
	if !chat.ValidID(a.ID) || !chat.ValidModel(a.Model) || a.Version <= 0 || a.CoveredSequence <= 0 {
		return chat.ErrInvalid
	}
	var err error
	a.Budget, err = canonicalJSON(a.Budget)
	if err != nil {
		return err
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	return r.insertRecord(ctx, a.ConversationID, hash, `INSERT INTO conversation_summaries(id,conversation_id,version,covered_sequence,text,model,budget,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, []any{a.ID, a.ConversationID, a.Version, a.CoveredSequence, a.Text, a.Model, []byte(a.Budget), hash}, "SELECT payload_digest FROM conversation_summaries WHERE id=$1", a.ID)
}
func (r *Repository) LatestSummary(ctx context.Context, c chat.ConversationID) (chat.Summary, error) {
	var a chat.Summary
	if !chat.ValidID(string(c)) {
		return a, chat.ErrInvalid
	}
	err := r.pool.QueryRow(ctx, "SELECT id::text,conversation_id::text,version,covered_sequence,text,model,budget FROM conversation_summaries WHERE conversation_id=$1 ORDER BY version DESC LIMIT 1", c).Scan(&a.ID, &a.ConversationID, &a.Version, &a.CoveredSequence, &a.Text, &a.Model, &a.Budget)
	return a, dbError(err)
}

// AppendEvent caps each payload at 64KiB (also DB-enforced). Only transient
// message.delta rows expire (latest 128 per conversation); durable facts remain.
func (r *Repository) AppendEvent(ctx context.Context, a chat.Event) error {
	if !chat.ValidID(a.ID) || (a.TurnID != "" && !chat.ValidID(string(a.TurnID))) || a.Type == "" || a.Attempt < 0 || a.LogicalStep < 0 || (a.Attempt == 0 && a.LogicalStep != 0) || len(a.Payload) == 0 || len(a.Payload) > 65536 {
		return chat.ErrInvalid
	}
	a.Sequence = 0
	var err error
	a.Payload, err = canonicalJSON(a.Payload)
	if err != nil {
		return err
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	var step, attempt any
	if a.Attempt > 0 {
		step = a.LogicalStep
		attempt = a.Attempt
	}
	return r.withConversation(ctx, a.ConversationID, func(tx pgx.Tx) error {
		err := insertRecord(ctx, tx, hash, `INSERT INTO events(id,conversation_id,turn_id,logical_step,attempt,type,payload,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, []any{a.ID, a.ConversationID, nullable(string(a.TurnID)), step, attempt, a.Type, []byte(a.Payload), hash}, "SELECT payload_digest FROM events WHERE id=$1", a.ID)
		if err != nil || a.Type != "message.delta" {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM events WHERE conversation_id=$1 AND type='message.delta' AND sequence < (SELECT sequence FROM events WHERE conversation_id=$1 AND type='message.delta' ORDER BY sequence DESC OFFSET 127 LIMIT 1)`, a.ConversationID)
		return err
	})
}
func (r *Repository) ListEvents(ctx context.Context, c chat.ConversationID, after int64, limit int) ([]chat.Event, error) {
	if !chat.ValidID(string(c)) || after < 0 || !limitOK(limit) {
		return nil, chat.ErrInvalid
	}
	rows, err := r.pool.Query(ctx, "SELECT id::text,coalesce(turn_id::text,''),coalesce(logical_step,0),coalesce(attempt,0),type,payload,sequence FROM events WHERE conversation_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3", c, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chat.Event{}
	for rows.Next() {
		a := chat.Event{ConversationID: c}
		if err = rows.Scan(&a.ID, &a.TurnID, &a.LogicalStep, &a.Attempt, &a.Type, &a.Payload, &a.Sequence); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
