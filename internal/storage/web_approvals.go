package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) PrepareWebActions(ctx context.Context, ref chat.TurnRef) (chat.ModelStepResult, error) {
	out := chat.ModelStepResult{}
	result, err := r.GetChatModelResult(ctx, ref)
	if err != nil {
		return out, err
	}
	for i, c := range result.Calls {
		scope := chat.ApprovalScope{ConversationID: ref.ConversationID, TurnID: ref.TurnID, ToolCallID: c.ID, ToolName: c.Name, ToolVersion: "1", ToolsetVersion: chat.WebToolset, ActionHash: c.ActionHash, ContentRef: c.ContentRef}
		call := chat.ToolCall{ID: c.ID, ConversationID: ref.ConversationID, TurnID: ref.TurnID, LogicalKey: fmt.Sprintf("model/%d/tool/%d", ref.Step, i), Name: c.Name, Arguments: c.Arguments, ActionHash: c.ActionHash, ParentWorkflowID: "web-chat/" + string(ref.ConversationID), Status: "pending"}
		if err = r.RecordToolCall(ctx, call); err != nil {
			return out, err
		}
		raw, _ := json.Marshal(scope)
		err = r.withConversation(ctx, ref.ConversationID, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE tool_calls SET web_scope=$3 WHERE conversation_id=$1 AND id=$2 AND (web_scope IS NULL OR web_scope=$3::jsonb)`, ref.ConversationID, c.ID, raw)
			if e == nil && tag.RowsAffected() != 1 {
				return ErrConflict
			}
			return e
		})
		if err != nil {
			return out, err
		}
		out.Actions = append(out.Actions, chat.WebAction{Scope: scope})
	}
	return out, nil
}
func (r *Repository) RecordWebApproval(ctx context.Context, a chat.WebApprovalRecord) error {
	p := a.Approval
	s := p.Scope
	if !chat.ValidID(p.ID) || !validWebScope(s) || p.Deadline.IsZero() {
		return chat.ErrInvalid
	}
	raw, _ := json.Marshal(s)
	hash, _ := digest(p)
	return r.withConversation(ctx, s.ConversationID, func(tx pgx.Tx) error {
		if a.Outcome == "" {
			_, err := tx.Exec(ctx, `INSERT INTO approvals(id,conversation_id,tool_call_id,action_hash,deadline,policy_revision,payload_digest,scope) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO NOTHING`, p.ID, s.ConversationID, s.ToolCallID, s.ActionHash, p.Deadline, s.PolicyRevision, hash, raw)
			if err != nil {
				return err
			}
			var same bool
			if err = tx.QueryRow(ctx, `SELECT scope=$3::jsonb AND deadline=$4 FROM approvals WHERE conversation_id=$1 AND id=$2`, s.ConversationID, p.ID, raw, p.Deadline).Scan(&same); err != nil {
				return err
			}
			if !same {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, `UPDATE turns SET status='awaiting_approval',updated_at=clock_timestamp() WHERE conversation_id=$1 AND id=$2 AND status='running'`, s.ConversationID, s.TurnID)
			return err
		}
		if a.Outcome != "authorized" && a.Outcome != "denied" && a.Outcome != "expired" && a.Outcome != "cancelled" && a.Outcome != "revoked" {
			return chat.ErrInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE approvals SET outcome=$3 WHERE conversation_id=$1 AND id=$2 AND scope=$4::jsonb AND (outcome IS NULL OR outcome=$3 OR ($3='cancelled' AND EXISTS(SELECT 1 FROM turns WHERE id=$5 AND conversation_id=$1 AND status IN ('cancelling','cancelled'))))`, s.ConversationID, p.ID, a.Outcome, raw, s.TurnID)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return err
	})
}
func recordWebPolicyReceipt(ctx context.Context, tx pgx.Tx, c chat.CommandIdentity, result chat.AdmissionResult) error {
	if result.PolicyRevision < 0 || len(result.GrantedToolset) > 128 {
		return chat.ErrInvalid
	}
	if c.Kind == chat.CommandDecide {
		tag, err := tx.Exec(ctx, `UPDATE approvals SET decision=$4,decided_at=coalesce(decided_at,clock_timestamp()) WHERE conversation_id=$1 AND id=$2 AND action_hash=$3 AND (decision IS NULL OR decision=$4)`, c.ConversationID, c.ApprovalID, c.ActionHash, c.Decision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	if c.Kind == chat.CommandRevoke || c.Decision == chat.ApprovalAll {
		_, err := tx.Exec(ctx, `UPDATE conversations SET policy_revision=$2,approval_toolset=$3 WHERE id=$1 AND policy_revision<=$2`, c.ConversationID, result.PolicyRevision, result.GrantedToolset)
		return err
	}
	return nil
}
func validWebScope(s chat.ApprovalScope) bool {
	return chat.ValidID(string(s.ConversationID)) && chat.ValidID(string(s.TurnID)) && chat.ValidID(s.ToolCallID) && chat.ValidHash(s.ActionHash) && chat.ValidHash(s.ContentRef) && s.ToolsetVersion == chat.WebToolset && s.ToolVersion == "1" && (s.ToolName == "web_fetch" || s.ToolName == "file_upload") && s.PolicyRevision >= 0
}
func (r *Repository) RecordWebAuthorization(ctx context.Context, a chat.WebAction) error {
	s := a.Scope
	if !validWebScope(s) || (a.Outcome != "authorized" && a.Outcome != "denied" && a.Outcome != "expired") {
		return chat.ErrInvalid
	}
	raw, _ := json.Marshal(s)
	base := s
	base.PolicyRevision = 0
	original, _ := json.Marshal(base)
	return r.withConversation(ctx, s.ConversationID, func(tx pgx.Tx) error {
		var status string
		var sealed bool
		if err := tx.QueryRow(ctx, `SELECT status,model_invocations_closed FROM turns WHERE conversation_id=$1 AND id=$2`, s.ConversationID, s.TurnID).Scan(&status, &sealed); err != nil {
			return err
		}
		if sealed || (status != "running" && status != "awaiting_approval") {
			return ErrStaleChatAttempt
		}
		if a.Outcome == "authorized" {
			tag, err := tx.Exec(ctx, `UPDATE tool_calls SET authorized_scope=$3,status='running' WHERE conversation_id=$1 AND id=$2 AND web_scope=$4::jsonb AND (authorized_scope IS NULL OR authorized_scope=$3::jsonb) AND status IN ('pending','running')`, s.ConversationID, s.ToolCallID, raw, original)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		} else {
			result, _ := json.Marshal(chat.WebToolResult{Error: a.Outcome})
			tag, err := tx.Exec(ctx, `UPDATE tool_calls SET status='denied',result=$3 WHERE conversation_id=$1 AND id=$2 AND web_scope=$4::jsonb AND authorized_scope IS NULL AND (status='pending' OR (status='denied' AND result=$3::jsonb))`, s.ConversationID, s.ToolCallID, result, original)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		}
		_, err := tx.Exec(ctx, `UPDATE turns SET status='running',updated_at=clock_timestamp() WHERE conversation_id=$1 AND id=$2 AND status='awaiting_approval'`, s.ConversationID, s.TurnID)
		return err
	})
}

// The per-body ledger uses the SAME turn seal as inference. A delayed delivery
// cannot start after sealing; a controlled target requires every cleanup ack.
func (r *Repository) BeginWebTool(ctx context.Context, s chat.ApprovalScope, id string, attempt int) (args json.RawMessage, cached bool, err error) {
	if !validWebScope(s) || !chat.ValidID(id) || attempt < 1 {
		return nil, false, chat.ErrInvalid
	}
	raw, _ := json.Marshal(s)
	err = r.withConversation(ctx, s.ConversationID, func(tx pgx.Tx) error {
		var status string
		var closed bool
		if e := tx.QueryRow(ctx, `SELECT status,model_invocations_closed FROM turns WHERE conversation_id=$1 AND id=$2`, s.ConversationID, s.TurnID).Scan(&status, &closed); e != nil {
			return e
		}
		if closed || status != "running" {
			return ErrStaleChatAttempt
		}
		if e := tx.QueryRow(ctx, `SELECT arguments,status FROM tool_calls WHERE conversation_id=$1 AND id=$2 AND turn_id=$3 AND authorized_scope=$4::jsonb`, s.ConversationID, s.ToolCallID, s.TurnID, raw).Scan(&args, &status); e != nil {
			return dbError(e)
		}
		cached = status == "completed" || status == "failed"
		_, e := tx.Exec(ctx, `INSERT INTO web_tool_invocations(id,conversation_id,turn_id,tool_call_id,action_hash,attempt) VALUES($1,$2,$3,$4,$5,$6)`, id, s.ConversationID, s.TurnID, s.ToolCallID, s.ActionHash, attempt)
		return e
	})
	return
}
func (r *Repository) SaveWebToolResult(ctx context.Context, s chat.ApprovalScope, id string, result chat.WebToolResult) error {
	if len(result.Text) > 16000 || len(result.Error) > 128 {
		return chat.ErrInvalid
	}
	raw, _ := json.Marshal(result)
	scope, _ := json.Marshal(s)
	return r.withConversation(ctx, s.ConversationID, func(tx pgx.Tx) error {
		var status string
		var owner string
		var same bool
		if err := tx.QueryRow(ctx, `SELECT status,coalesce(result_owner::text,''),coalesce(result=$4::jsonb,false) FROM tool_calls WHERE conversation_id=$1 AND id=$2 AND authorized_scope=$3::jsonb`, s.ConversationID, s.ToolCallID, scope, raw).Scan(&status, &owner, &same); err != nil {
			return err
		}
		if status == "completed" || status == "failed" {
			if owner == id && !same {
				return ErrConflict
			}
			return nil
		} // A different attempt may adopt, but the owner cannot change its result.
		var owns bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM web_tool_invocations i JOIN turns t ON t.id=i.turn_id WHERE i.id=$1 AND i.tool_call_id=$2 AND i.status='running' AND t.status='running' AND NOT EXISTS(SELECT 1 FROM web_tool_invocations n WHERE n.tool_call_id=i.tool_call_id AND n.attempt>i.attempt))`, id, s.ToolCallID).Scan(&owns); err != nil {
			return err
		}
		if !owns {
			return ErrStaleChatAttempt
		}
		status = "completed"
		if result.Error != "" {
			status = "failed"
		}
		if _, err := tx.Exec(ctx, `UPDATE tool_calls SET status=$3,result=$4,result_owner=$5 WHERE conversation_id=$1 AND id=$2`, s.ConversationID, s.ToolCallID, status, raw, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE web_tool_invocations SET status=$2 WHERE id=$1`, id, status)
		return err
	})
}
func (r *Repository) AcknowledgeWebToolCleanup(ctx context.Context, s chat.ApprovalScope, id string) error {
	return r.withConversation(ctx, s.ConversationID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE web_tool_invocations SET cleanup_confirmed_at=coalesce(cleanup_confirmed_at,clock_timestamp()) WHERE id=$1 AND conversation_id=$2 AND turn_id=$3 AND tool_call_id=$4`, id, s.ConversationID, s.TurnID, s.ToolCallID)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return err
	})
}
func (r *Repository) WebToolResult(ctx context.Context, c chat.ConversationID, id string) (chat.WebToolResult, error) {
	var out chat.WebToolResult
	var raw []byte
	err := r.pool.QueryRow(ctx, `SELECT result FROM tool_calls WHERE conversation_id=$1 AND id=$2 AND status IN ('completed','failed','denied')`, c, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, dbError(err)
}

// Metadata-only lookup: this path never selects extracted text or original bytes.
func (r *Repository) WebAttachmentChecksum(ctx context.Context, c chat.ConversationID, id chat.AttachmentID) (string, error) {
	var checksum string
	err := r.pool.QueryRow(ctx, `SELECT checksum FROM attachments WHERE conversation_id=$1 AND id=$2 AND status='ready'`, c, id).Scan(&checksum)
	return checksum, dbError(err)
}
