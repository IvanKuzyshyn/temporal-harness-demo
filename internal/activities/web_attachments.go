package activities

import (
	"context"
	"encoding/json"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
)

// The store independently verifies the persisted authorization/registration
// before even a cache read. Canonical args and hash are checked before entry.
func NewWebAttachmentAdapter(store *attachments.Store) tools.WebAdapter {
	return func(ctx context.Context, s chat.ApprovalScope, args json.RawMessage) (chat.WebToolResult, error) {
		if store == nil || s.ToolName != "file_upload" || s.ToolVersion != "1" || s.ToolsetVersion != chat.WebToolset {
			return chat.WebToolResult{}, chat.ErrInvalid
		}
		call, e := tools.NewWebRegistry(nil).Canonicalize("file_upload", args, s.ContentRef)
		if e != nil {
			return chat.WebToolResult{}, e
		}
		call.ID = s.ToolCallID
		raw, _ := json.Marshal(struct {
			Conversation chat.ConversationID
			Turn         chat.TurnID
			Set          string
			Call         chat.WebModelCall
		}{s.ConversationID, s.TurnID, chat.WebToolset, call})
		if tools.WebDigest(raw) != s.ActionHash {
			return chat.WebToolResult{}, chat.ErrInvalid
		}
		var in struct {
			ID chat.AttachmentID `json:"attachmentId"`
		}
		if e = json.Unmarshal(call.Arguments, &in); e != nil {
			return chat.WebToolResult{}, e
		}
		doc, e := store.Extract(ctx, s, in.ID)
		if e != nil {
			return chat.WebToolResult{}, e
		}
		text := bounded(doc.Text, 16000)
		out := chat.WebToolResult{Text: text, AttachmentID: in.ID, Pages: doc.Pages, Truncated: doc.Truncated || len(text) < len(doc.Text), ExtractionStatus: doc.Status}
		if doc.Status == "unsupported" {
			out.Error = doc.Reason
		}
		return out, nil
	}
}
