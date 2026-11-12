package storage

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) CheckAttachmentConversation(ctx context.Context, c chat.ConversationID) error {
	_, e := r.GetConversation(ctx, c)
	return e
}

// Authorization is rechecked at both read and publication. The adapter also
// verifies canonical action hashing; this query binds the actual persisted args,
// immutable checksum and a registered, not-yet-joined tool body to that scope.
const attachmentAuthorization = `SELECT EXISTS(
 SELECT 1 FROM tool_calls t JOIN attachments a ON a.conversation_id=t.conversation_id AND a.id=$3
 JOIN turns tr ON tr.id=t.turn_id AND tr.conversation_id=t.conversation_id
 WHERE t.conversation_id=$1 AND t.id=$2 AND t.authorized_scope=$4::jsonb
 AND t.name='file_upload' AND t.arguments->>'attachmentId'=a.id::text
 AND a.checksum=$5 AND a.status='ready' AND tr.status='running'
 AND EXISTS(SELECT 1 FROM web_tool_invocations i WHERE i.tool_call_id=t.id AND i.cleanup_confirmed_at IS NULL))`

func authorizeAttachment(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, s chat.ApprovalScope, id chat.AttachmentID) error {
	if !validWebScope(s) || s.ToolName != "file_upload" || !chat.ValidID(string(id)) {
		return chat.ErrInvalid
	}
	raw, _ := json.Marshal(s)
	var ok bool
	if e := q.QueryRow(ctx, attachmentAuthorization, s.ConversationID, s.ToolCallID, id, raw, s.ContentRef).Scan(&ok); e != nil {
		return e
	}
	if !ok {
		return ErrConflict
	}
	return nil
}
func (r *Repository) AuthorizeAttachment(ctx context.Context, s chat.ApprovalScope, id chat.AttachmentID) error {
	return authorizeAttachment(ctx, r.pool, s, id)
}
func (r *Repository) ReadAttachmentExtraction(ctx context.Context, c chat.ConversationID, id chat.AttachmentID) (chat.ExtractedDocument, error) {
	var out chat.ExtractedDocument
	var raw []byte
	e := r.pool.QueryRow(ctx, "SELECT extraction_result FROM attachments WHERE conversation_id=$1 AND id=$2 AND extraction_result IS NOT NULL", c, id).Scan(&raw)
	if e == nil {
		e = json.Unmarshal(raw, &out)
	}
	return out, dbError(e)
}
func (r *Repository) SaveAttachmentExtraction(ctx context.Context, s chat.ApprovalScope, id chat.AttachmentID, d chat.ExtractedDocument) error {
	if !utf8.ValidString(d.Text) || strings.ContainsRune(d.Text, 0) || utf8.RuneCountInString(d.Text) > 200000 || len(d.Reason) > 128 || d.Pages < 0 || len(d.Sections) > 100 || (d.Status != "completed" && d.Status != "unsupported") {
		return chat.ErrInvalid
	}
	for _, section := range d.Sections {
		if section.Start < 0 || section.End < section.Start || section.End > utf8.RuneCountInString(d.Text) || section.Page < 0 || section.Page > 100 {
			return chat.ErrInvalid
		}
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return e
	}
	return r.withConversation(ctx, s.ConversationID, func(tx pgx.Tx) error {
		if e := authorizeAttachment(ctx, tx, s, id); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE attachments SET extraction_result=$3,extracted_text=$4,extraction_status=$5 WHERE conversation_id=$1 AND id=$2 AND (extraction_result IS NULL OR extraction_result=$3::jsonb)`, s.ConversationID, id, raw, d.Text, d.Status)
		if e == nil && tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return e
	})
}
