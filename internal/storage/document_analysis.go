package storage

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ReadDocumentPart(ctx context.Context, in chat.DocumentAnalysisInput, attachment chat.AttachmentID, index int) (chat.DocumentPart, error) {
	var out chat.DocumentPart
	if !in.Valid() || index < -1 || index >= in.Budget.MaxChunks {
		return out, chat.ErrInvalid
	}
	// Authorization must precede cache access, including a different instruction
	// for the same original bytes. A filename is never an analysis cache key.
	if err := r.AuthorizeAttachment(ctx, in.Scope, attachment); err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	var result []byte
	err := r.pool.QueryRow(ctx, `SELECT result FROM document_analysis_parts WHERE tool_call_id=$1 AND part_index=$2 AND input=$3::jsonb`, in.Scope.ToolCallID, index, raw).Scan(&result)
	if err == nil {
		err = json.Unmarshal(result, &out)
		if err == nil && index >= 0 && !out.ValidSummary() {
			err = chat.ErrInvalid
		}
	}
	return out, dbError(err)
}
func (r *Repository) SaveDocumentPart(ctx context.Context, in chat.DocumentAnalysisInput, attachment chat.AttachmentID, index int, owner string, out chat.DocumentPart) error {
	if !in.Valid() || index < -1 || index >= in.Budget.MaxChunks || (index >= 0 && !out.ValidSummary()) || !chat.ValidID(owner) || !utf8.ValidString(out.Text) || len(out.Text) > 1000 || len(out.Reason) > 128 || out.Pages < 0 || out.Manifest.Chunks < 0 || out.Manifest.Chunks > 32 {
		return chat.ErrInvalid
	}
	raw, _ := json.Marshal(in)
	result, _ := json.Marshal(out)
	return r.withConversation(ctx, in.Scope.ConversationID, func(tx pgx.Tx) error {
		if err := authorizeAttachment(ctx, tx, in.Scope, attachment); err != nil {
			return err
		}
		var owns bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM web_tool_invocations WHERE id=$1 AND tool_call_id=$2 AND cleanup_confirmed_at IS NULL)`, owner, in.Scope.ToolCallID).Scan(&owns); err != nil {
			return err
		}
		if !owns {
			return ErrConflict
		}
		_, err := tx.Exec(ctx, `INSERT INTO document_analysis_parts(tool_call_id,part_index,input,result,owner) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, in.Scope.ToolCallID, index, raw, result, owner)
		if err != nil {
			return err
		}
		var same bool
		// Another retry's canonical winner may be adopted; the winning body cannot
		// mutate its own result. No re-execution of completed chunks is necessary.
		err = tx.QueryRow(ctx, `SELECT input=$3::jsonb AND (owner<>$4 OR result=$5::jsonb) FROM document_analysis_parts WHERE tool_call_id=$1 AND part_index=$2`, in.Scope.ToolCallID, index, raw, owner, result).Scan(&same)
		if err == nil && !same {
			return ErrConflict
		}
		return err
	})
}
