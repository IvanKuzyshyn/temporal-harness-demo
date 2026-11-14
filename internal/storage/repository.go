// Package storage persists application content and durable command intent.
// Execution/approval state is a projection, not permission to execute tools.
package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("identity payload conflict")
var ErrNotFound = errors.New("not found")

type Repository struct{ pool *pgxpool.Pool }

// New borrows a pool. Its owner is responsible for closing it.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }
func dbError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, p.ConstraintName)
	}
	return err
}
func digest(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func canonicalJSON(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, chat.ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, chat.ErrInvalid
	}
	return json.Marshal(v)
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func limitOK(n int) bool { return n > 0 && n <= 200 }
func (r *Repository) withConversation(ctx context.Context, id chat.ConversationID, f func(pgx.Tx) error) error {
	if !chat.ValidID(string(id)) {
		return chat.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var found string
	if err = tx.QueryRow(ctx, "SELECT id::text FROM conversations WHERE id=$1 FOR UPDATE", id).Scan(&found); err != nil {
		return dbError(err)
	}
	if err = f(tx); err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit(ctx))
}

const conversationColumns = "id::text,title,model,workflow_id,lifecycle,policy_revision,created_at,updated_at"

func scanConversation(row pgx.Row) (chat.Conversation, error) {
	var c chat.Conversation
	err := row.Scan(&c.ID, &c.Title, &c.Model, &c.WorkflowID, &c.Lifecycle, &c.PolicyRevision, &c.CreatedAt, &c.UpdatedAt)
	return c, dbError(err)
}
func (r *Repository) CreateConversation(ctx context.Context, model string) (chat.Conversation, error) {
	if !chat.ValidModel(model) {
		return chat.Conversation{}, chat.ErrInvalid
	}
	id := uuid.NewString()
	return scanConversation(r.pool.QueryRow(ctx, "INSERT INTO conversations(id,model,workflow_id) VALUES($1,$2,$3) RETURNING "+conversationColumns, id, model, "web-chat/"+id))
}
func (r *Repository) GetConversation(ctx context.Context, id chat.ConversationID) (chat.Conversation, error) {
	if !chat.ValidID(string(id)) {
		return chat.Conversation{}, chat.ErrInvalid
	}
	return scanConversation(r.pool.QueryRow(ctx, "SELECT "+conversationColumns+" FROM conversations WHERE id=$1", id))
}

const receiptColumns = "id::text,coalesce(turn_id::text,''),payload_digest,dispatch_state,result_state,result"

func scanReceipt(row pgx.Row) (chat.CommandReceipt, error) {
	var a chat.CommandReceipt
	err := row.Scan(&a.CommandID, &a.TurnID, &a.PayloadDigest, &a.DispatchState, &a.ResultState, &a.Result)
	return a, err
}

// AcceptCommand atomically records intent plus a user message/turn for message
// and replace. Expected-active and approval hashes bind ownership, NOT freshness
// or authorization: only the workflow may adjudicate those.
func (r *Repository) AcceptCommand(ctx context.Context, in chat.CommandInput) (out chat.CommandReceipt, err error) {
	hash, err := in.Digest()
	if err != nil {
		return out, err
	}
	if in.PayloadDigest != "" && in.PayloadDigest != hash {
		return out, ErrConflict
	}
	err = r.withConversation(ctx, in.ConversationID, func(tx pgx.Tx) error {
		old, e := scanReceipt(tx.QueryRow(ctx, "SELECT "+receiptColumns+" FROM commands WHERE conversation_id=$1 AND request_id=$2", in.ConversationID, in.RequestID))
		if e == nil {
			if old.PayloadDigest != hash {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var turn chat.TurnID
		if in.Kind == chat.CommandMessage || in.Kind == chat.CommandReplace {
			var model string
			if e := tx.QueryRow(ctx, "SELECT model FROM conversations WHERE id=$1", in.ConversationID).Scan(&model); e != nil {
				return e
			}
			if !chat.ValidModel(model) {
				return chat.ErrInvalid
			}
			turn = chat.TurnID(uuid.NewString())
			kind := "message"
			if in.Kind == chat.CommandReplace {
				kind = "replacement"
			}
			_, e = tx.Exec(ctx, "INSERT INTO turns(id,conversation_id,request_id,kind,replacement_target) VALUES($1,$2,$3,$4,$5)", turn, in.ConversationID, in.RequestID, kind, nullable(string(in.Payload.ExpectedActiveTurnID)))
			if e != nil {
				return e
			}
			m := chat.MessageRecord{EntryID: uuid.NewString(), ConversationID: in.ConversationID, TurnID: turn, Role: "user", Text: in.Payload.Text, AttachmentIDs: in.Payload.AttachmentIDs, ResultKind: "prompt", CompletionState: "completed"}
			if e = appendMessage(ctx, tx, m); e != nil {
				return e
			}
			title := strings.Join(strings.Fields(in.Payload.Text), " ")
			runes := []rune(title)
			if len(runes) > 80 {
				title = string(runes[:80])
			}
			if title == "" {
				title = "Attachment"
			}
			_, e = tx.Exec(ctx, "UPDATE conversations SET title=$2 WHERE id=$1 AND title=''", in.ConversationID, title)
			if e != nil {
				return e
			}
		}
		payload, e := json.Marshal(in.Payload)
		if e != nil {
			return e
		}
		out, e = scanReceipt(tx.QueryRow(ctx, `INSERT INTO commands(id,conversation_id,request_id,turn_id,kind,expected_active_turn_id,approval_id,action_hash,payload,payload_digest)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+receiptColumns, uuid.NewString(), in.ConversationID, in.RequestID, nullable(string(turn)), in.Kind, nullable(string(in.Payload.ExpectedActiveTurnID)), nullable(in.Payload.ApprovalID), nullable(in.Payload.ActionHash), payload, hash))
		return e
	})
	return out, err
}
func validateMessage(m chat.MessageRecord) error {
	if !chat.ValidID(m.EntryID) || !chat.ValidID(string(m.TurnID)) || !chat.ValidID(string(m.ConversationID)) || m.LogicalStep < 0 || m.ResultKind == "" || (m.Role != "user" && m.Role != "assistant") || (m.CompletionState != "completed" && m.CompletionState != "incomplete") || len(m.AttachmentIDs) > chat.MaxAttachments {
		return chat.ErrInvalid
	}
	seen := map[chat.AttachmentID]bool{}
	for _, id := range m.AttachmentIDs {
		if !chat.ValidID(string(id)) || seen[id] {
			return chat.ErrInvalid
		}
		seen[id] = true
	}
	return nil
}
func appendMessage(ctx context.Context, tx pgx.Tx, m chat.MessageRecord) error {
	if err := validateMessage(m); err != nil {
		return err
	}
	// Server-assigned order and time are not part of write identity.
	m.Sequence = 0
	m.Context = nil // derived from the immutable final model result on reads
	m.TurnStatus, m.TurnReason = "", ""
	m.CreatedAt = chat.MessageRecord{}.CreatedAt
	if len(m.AttachmentIDs) == 0 {
		m.AttachmentIDs = nil
	}
	hash, err := digest(m)
	if err != nil {
		return err
	}
	var existing string
	err = tx.QueryRow(ctx, "SELECT payload_digest FROM messages WHERE entry_id=$1 OR (turn_id=$2 AND logical_step=$3 AND result_kind=$4)", m.EntryID, m.TurnID, m.LogicalStep, m.ResultKind).Scan(&existing)
	if err == nil {
		if existing != hash {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO messages(entry_id,conversation_id,turn_id,role,text,logical_step,result_kind,completion_state,payload_digest)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, m.EntryID, m.ConversationID, m.TurnID, m.Role, m.Text, m.LogicalStep, m.ResultKind, m.CompletionState, hash)
	if err != nil {
		return err
	}
	for i, id := range m.AttachmentIDs {
		_, err = tx.Exec(ctx, "INSERT INTO message_attachments(conversation_id,entry_id,attachment_id,ordinal) VALUES($1,$2,$3,$4)", m.ConversationID, m.EntryID, id, i)
		if err != nil {
			return err
		}
	}
	return nil
}
func (r *Repository) AppendMessage(ctx context.Context, m chat.MessageRecord) error {
	return r.withConversation(ctx, m.ConversationID, func(tx pgx.Tx) error { return appendMessage(ctx, tx, m) })
}

// ListMessages returns a page in ascending commit order. Before is an exclusive,
// conversation-bound high-water mark; later appends cannot enter older pages.
func (r *Repository) ListMessages(ctx context.Context, id chat.ConversationID, before string, limit int) (chat.MessagePage, error) {
	return listMessages(ctx, r.pool, id, before, limit)
}

type readQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func listMessages(ctx context.Context, q readQuery, id chat.ConversationID, before string, limit int) (chat.MessagePage, error) {
	page := chat.MessagePage{Messages: []chat.MessageRecord{}}
	if !chat.ValidID(string(id)) || !limitOK(limit) {
		return page, chat.ErrInvalid
	}
	boundary := int64(9223372036854775807)
	if before != "" {
		raw, e := base64.RawURLEncoding.DecodeString(before)
		if e != nil {
			return page, chat.ErrInvalid
		}
		parts := strings.Split(string(raw), ":")
		if len(parts) != 3 || parts[0] != "v1" || parts[1] != string(id) {
			return page, chat.ErrInvalid
		}
		boundary, e = strconv.ParseInt(parts[2], 10, 64)
		if e != nil || boundary <= 0 {
			return page, chat.ErrInvalid
		}
	}
	rows, err := q.Query(ctx, `SELECT m.entry_id::text,m.turn_id::text,m.sequence,m.role,m.text,m.logical_step,m.result_kind,m.completion_state,m.created_at,
 coalesce((SELECT jsonb_agg(attachment_id::text ORDER BY ordinal) FROM message_attachments WHERE entry_id=m.entry_id),'[]'::jsonb),
 (SELECT context_metadata FROM chat_model_results r WHERE r.turn_id=m.turn_id AND m.result_kind='final' AND m.role='assistant' AND r.calls='[]'::jsonb ORDER BY r.logical_step DESC LIMIT 1)
, t.status,t.reason
 FROM messages m JOIN turns t ON t.id=m.turn_id AND t.conversation_id=m.conversation_id WHERE m.conversation_id=$1 AND m.sequence<$2 ORDER BY m.sequence DESC LIMIT $3`, id, boundary, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		m := chat.MessageRecord{ConversationID: id}
		var ids []byte
		err = rows.Scan(&m.EntryID, &m.TurnID, &m.Sequence, &m.Role, &m.Text, &m.LogicalStep, &m.ResultKind, &m.CompletionState, &m.CreatedAt, &ids, &m.Context, &m.TurnStatus, &m.TurnReason)
		if err != nil {
			return page, err
		}
		if err = json.Unmarshal(ids, &m.AttachmentIDs); err != nil {
			return page, err
		}
		page.Messages = append(page.Messages, m)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Messages) > limit {
		page.Messages = page.Messages[:limit]
		page.Before = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v1:%s:%d", id, page.Messages[len(page.Messages)-1].Sequence)))
	}
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	return page, nil
}

// ListPendingCommands may be consumed by multiple dispatchers. Delivery is at
// least once: use CommandID as the stable Temporal Update ID, never a new UUID.
func (r *Repository) ListPendingCommands(ctx context.Context, limit int) ([]chat.StoredCommand, error) {
	if !limitOK(limit) {
		return nil, chat.ErrInvalid
	}
	rows, err := r.pool.Query(ctx, "SELECT "+receiptColumns+",conversation_id::text,request_id::text,kind,payload FROM commands WHERE dispatch_state='pending' OR result_state='pending' ORDER BY created_at,id LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chat.StoredCommand{}
	for rows.Next() {
		var c chat.StoredCommand
		var payload []byte
		a := &c.Receipt
		in := &c.Input
		if err = rows.Scan(&a.CommandID, &a.TurnID, &a.PayloadDigest, &a.DispatchState, &a.ResultState, &a.Result, &in.ConversationID, &in.RequestID, &in.Kind, &payload); err != nil {
			return nil, err
		}
		in.PayloadDigest = a.PayloadDigest
		if err = json.Unmarshal(payload, &in.Payload); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordCommandResult stores a gateway projection; terminal receipts are immutable.
// It does not update turn execution state or grant approval.
func (r *Repository) RecordCommandResult(ctx context.Context, c chat.ConversationID, id chat.CommandID, dispatch, result string, body []byte) error {
	if !chat.ValidID(string(id)) || (dispatch != "pending" && dispatch != "dispatched") || (result != "pending" && result != "accepted" && result != "rejected") || (result != "pending" && dispatch != "dispatched") {
		return chat.ErrInvalid
	}
	canonical, err := canonicalJSON(body)
	if err != nil {
		return err
	}
	return r.withConversation(ctx, c, func(tx pgx.Tx) error {
		// Keep the existing conversation -> command lock order. SettleClaim
		// locks only the command row, so the receipt must be locked before its
		// terminal-state inspection; the conversation lock alone cannot fence it.
		old, e := scanReceipt(tx.QueryRow(ctx, "SELECT "+receiptColumns+" FROM commands WHERE conversation_id=$1 AND id=$2 FOR UPDATE", c, id))
		if e != nil {
			return e
		}
		if old.ResultState != "pending" {
			// JSONB normalizes numeric notation (1e3 -> 1000). Compare in
			// its own domain instead of treating a DB round trip as a change.
			var same bool
			if e = tx.QueryRow(ctx, "SELECT result IS NOT DISTINCT FROM $3::jsonb FROM commands WHERE conversation_id=$1 AND id=$2", c, id, canonical).Scan(&same); e != nil {
				return e
			}
			if old.DispatchState == dispatch && old.ResultState == result && same {
				return nil
			}
			return ErrConflict
		}
		if old.DispatchState == "dispatched" && dispatch == "pending" {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, "UPDATE commands SET dispatch_state=$3,result_state=$4,result=$5,updated_at=clock_timestamp() WHERE conversation_id=$1 AND id=$2", c, id, dispatch, result, canonical)
		return e
	})
}
