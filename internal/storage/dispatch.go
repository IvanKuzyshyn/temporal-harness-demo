package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrLeaseLost = errors.New("command lease lost")

// CommandClaim is a fenced, expiring right to acknowledge an at-least-once
// delivery. Token is never an execution identity; Command.Receipt.CommandID is.
type CommandClaim struct {
	Command                  chat.StoredCommand
	WorkflowID, Model, Token string
	Sequence                 int64
	Attempts                 int
}

// ClaimCommands never holds SQL locks across network calls. Normal messages
// cannot overtake unresolved earlier normal messages. Controls (including
// replacement) bypass that lane so a stuck message cannot suppress stop/consent.
// Due-time ordering plus backoff keeps a failed oldest row from monopolizing work.
func (r *Repository) ClaimCommands(ctx context.Context, limit int, lease time.Duration) ([]CommandClaim, error) {
	if !limitOK(limit) || lease < time.Second || lease > time.Minute {
		return nil, chat.ErrInvalid
	}
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
 SELECT c.id FROM commands c
 WHERE c.result_state='pending' AND c.next_attempt_at<=clock_timestamp()
 AND (c.lease_until IS NULL OR c.lease_until<=clock_timestamp())
 AND (c.kind<>'message' OR NOT EXISTS (
 SELECT 1 FROM commands older WHERE older.conversation_id=c.conversation_id
 AND older.kind='message' AND older.sequence<c.sequence AND older.result_state='pending'))
 ORDER BY c.next_attempt_at,c.created_at,c.id FOR UPDATE OF c SKIP LOCKED LIMIT $1
 ), claimed AS (
 UPDATE commands c SET lease_token=$2,lease_until=clock_timestamp()+$3*interval '1 millisecond',attempts=c.attempts+1
 FROM candidates x WHERE c.id=x.id RETURNING c.*)
 SELECT c.id::text,coalesce(c.turn_id::text,''),c.payload_digest,c.dispatch_state,c.result_state,c.result,
 c.conversation_id::text,c.request_id::text,c.kind,c.payload,v.workflow_id,v.model,c.sequence,c.lease_token::text,c.attempts
 FROM claimed c JOIN conversations v ON v.id=c.conversation_id ORDER BY c.next_attempt_at,c.created_at,c.id`, limit, uuid.NewString(), lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandClaim{}
	for rows.Next() {
		var c CommandClaim
		var payload []byte
		a := &c.Command.Receipt
		in := &c.Command.Input
		if err = rows.Scan(&a.CommandID, &a.TurnID, &a.PayloadDigest, &a.DispatchState, &a.ResultState, &a.Result, &in.ConversationID, &in.RequestID, &in.Kind, &payload, &c.WorkflowID, &c.Model, &c.Sequence, &c.Token, &c.Attempts); err != nil {
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

// SettleClaim atomically acknowledges or reschedules, only for the current,
// unexpired owner. state=="" means a transport/ambiguous failure: preserve the
// previous durable receipt. No retries are deleted, capped, or dead-lettered.
func (r *Repository) SettleClaim(ctx context.Context, c CommandClaim, state string, body []byte, retry time.Duration) error {
	if !chat.ValidID(string(c.Command.Receipt.CommandID)) || !chat.ValidID(c.Token) || retry < time.Millisecond || retry > time.Hour || (state != "" && state != "pending" && state != "accepted" && state != "rejected") {
		return chat.ErrInvalid
	}
	if len(body) > 65536 {
		return chat.ErrInvalid
	}
	canonical, err := canonicalJSON(body)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `UPDATE commands SET
 dispatch_state=CASE WHEN $3='' THEN dispatch_state ELSE 'dispatched' END,
 result_state=CASE WHEN $3='' THEN result_state ELSE $3 END,
 result=CASE WHEN $3='' THEN result ELSE $4::jsonb END,
 updated_at=clock_timestamp(),lease_token=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()+$5*interval '1 millisecond'
 WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() AND result_state='pending'`, c.Command.Receipt.CommandID, c.Token, state, canonical, retry.Milliseconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (r *Repository) GetCommand(ctx context.Context, c chat.ConversationID, id chat.CommandID) (chat.CommandReceipt, error) {
	if !chat.ValidID(string(c)) || !chat.ValidID(string(id)) {
		return chat.CommandReceipt{}, chat.ErrInvalid
	}
	out, err := scanReceipt(r.pool.QueryRow(ctx, "SELECT "+receiptColumns+" FROM commands WHERE conversation_id=$1 AND id=$2", c, id))
	return out, dbError(err)
}
func (r *Repository) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }

// CreateConversationOnce deduplicates creation in a global, permanent request
// domain, separate from each conversation's command domain.
func (r *Repository) CreateConversationOnce(ctx context.Context, requestID, model string) (chat.Conversation, error) {
	if !chat.ValidID(requestID) || !chat.ValidModel(model) {
		return chat.Conversation{}, chat.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return chat.Conversation{}, err
	}
	defer tx.Rollback(context.Background())
	id := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO conversations(id,model,workflow_id,create_request_id) VALUES($1,$2,$3,$4) ON CONFLICT(create_request_id) DO NOTHING`, id, model, "web-chat/"+id, requestID)
	if err != nil {
		return chat.Conversation{}, dbError(err)
	}
	c, err := scanConversation(tx.QueryRow(ctx, "SELECT "+conversationColumns+" FROM conversations WHERE create_request_id=$1", requestID))
	if err != nil {
		return c, err
	}
	if c.Model != model {
		return chat.Conversation{}, ErrConflict
	}
	return c, tx.Commit(ctx)
}

// Projection is a read model, never proof of workflow freshness or permission.
// A repeatable-read snapshot prevents internally inconsistent metadata reads.
type TurnProjection struct {
	ID        chat.TurnID `json:"id"`
	Status    string      `json:"status"`
	UpdatedAt time.Time   `json:"updatedAt"`
}
type ApprovalProjection struct {
	ToolName       string          `json:"toolName"`
	Arguments      json.RawMessage `json:"arguments"`
	ContentRef     string          `json:"contentRef"`
	ToolsetVersion string          `json:"toolsetVersion"`
	ID             string          `json:"id"`
	ToolCallID     string          `json:"toolCallId"`
	ActionHash     string          `json:"actionHash"`
	Deadline       time.Time       `json:"deadline"`
	PolicyRevision int64           `json:"policyRevision"`
}
type ConversationProjection struct {
	ApprovalToolset string
	Conversation    chat.Conversation
	ActiveTurn      *TurnProjection
	PendingApproval *ApprovalProjection
}

func (r *Repository) GetConversationProjection(ctx context.Context, id chat.ConversationID) (ConversationProjection, error) {
	var out ConversationProjection
	if !chat.ValidID(string(id)) {
		return out, chat.ErrInvalid
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.Background())
	out.Conversation, err = scanConversation(tx.QueryRow(ctx, "SELECT "+conversationColumns+" FROM conversations WHERE id=$1", id))
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT approval_toolset FROM conversations WHERE id=$1`, id).Scan(&out.ApprovalToolset); err != nil {
		return out, err
	}
	var turn TurnProjection
	err = tx.QueryRow(ctx, "SELECT id::text,status,updated_at FROM turns WHERE conversation_id=$1 AND status IN ('running','awaiting_approval','cancelling') ORDER BY created_at,id LIMIT 1", id).Scan(&turn.ID, &turn.Status, &turn.UpdatedAt)
	if err == nil {
		out.ActiveTurn = &turn
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var a ApprovalProjection
	err = tx.QueryRow(ctx, `SELECT a.id::text,a.tool_call_id::text,a.action_hash,a.deadline,a.policy_revision,t.name,t.arguments,coalesce(a.scope->>'ContentRef',''),coalesce(a.scope->>'ToolsetVersion','') FROM approvals a
 JOIN tool_calls t ON t.id=a.tool_call_id JOIN turns u ON u.id=t.turn_id
 WHERE a.conversation_id=$1 AND a.decision IS NULL AND a.outcome IS NULL AND a.deadline>clock_timestamp()
 AND u.status='awaiting_approval' ORDER BY a.created_at,a.id LIMIT 1`, id).Scan(&a.ID, &a.ToolCallID, &a.ActionHash, &a.Deadline, &a.PolicyRevision, &a.ToolName, &a.Arguments, &a.ContentRef, &a.ToolsetVersion)
	if err == nil {
		out.PendingApproval = &a
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, tx.Commit(ctx)
}
