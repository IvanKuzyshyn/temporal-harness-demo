package attachments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
)

type extractionRepository interface {
	AuthorizeAttachment(context.Context, chat.ApprovalScope, chat.AttachmentID) error
	ReadAttachmentExtraction(context.Context, chat.ConversationID, chat.AttachmentID) (chat.ExtractedDocument, error)
	SaveAttachmentExtraction(context.Context, chat.ApprovalScope, chat.AttachmentID, chat.ExtractedDocument) error
}

// Extract requires the complete immutable authorized scope, not just an ID.
// Pure extraction may be cached by checksum, but every new action is authorized
// again before even reading that cache. No model call occurs in this primitive.
func (s *Store) Extract(ctx context.Context, scope chat.ApprovalScope, id chat.AttachmentID) (Document, error) {
	var out Document
	r, ok := s.repo.(extractionRepository)
	if !ok {
		return out, ErrInvalid
	}
	if e := r.AuthorizeAttachment(ctx, scope, id); e != nil {
		return out, e
	}
	out, e := r.ReadAttachmentExtraction(ctx, scope.ConversationID, id)
	if e == nil {
		return out, nil
	}
	if !errors.Is(e, storage.ErrNotFound) {
		return Document{}, fmt.Errorf("%w: %v", ErrPersistence, e)
	}
	a, e := s.repo.GetAttachment(ctx, scope.ConversationID, id)
	if e != nil {
		return Document{}, e
	}
	if a.Checksum != scope.ContentRef || a.Status != "ready" {
		return Document{}, ErrInvalid
	}
	b, e := s.readOriginal(a)
	if e != nil {
		return Document{}, e
	}
	out = Document{Status: "completed"}
	switch a.MediaType {
	case "text/plain", "text/markdown":
		if !utf8.Valid(b) {
			return Document{}, ErrInvalid
		}
		text := []rune(string(b))
		if len(text) > 200000 {
			text = text[:200000]
			out.Truncated = true
		}
		out.Text = string(text)
		if strings.TrimSpace(out.Text) == "" {
			out.Status = "unsupported"
			out.Reason = "empty_document"
		}
	case "application/pdf", docxType:
		out, e = s.parser.Parse(ctx, b, a.MediaType, true)
		if errors.Is(e, ErrInvalid) || (errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil) {
			out = Document{Status: "unsupported", Reason: "unreadable_or_resource_limited_document"}
			e = nil
		}
		if e != nil {
			return Document{}, e
		}
	default:
		out.Status = "unsupported"
		out.Reason = "image_analysis_not_qualified"
	}
	if len(out.Sections) == 0 && out.Text != "" {
		out.Sections = []chat.DocumentSection{{Start: 0, End: utf8.RuneCountInString(out.Text)}}
	}
	if e = ctx.Err(); e != nil {
		return Document{}, e
	}
	if e = r.SaveAttachmentExtraction(ctx, scope, id, out); e != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrPersistence, e)
	}
	return out, nil
}
