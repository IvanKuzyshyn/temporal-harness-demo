package activities

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
)

// Same immutable action and physical-body ledger as document chunks. Nothing
// except references/coverage returns to workflow history. A new instruction is
// a new approved action; a persisted prior result never authorizes original reads.
func (a *ChatActivities) AnalyzeAttachmentImage(ctx context.Context, in chat.DocumentAnalysisInput) (out chat.DocumentAnalysisResult, err error) {
	err = a.documentBody(ctx, in, func(parent context.Context, w WebRepository, d documentRepository, call tools.AttachmentAnalysisArgs, id string) error {
		part, e := d.ReadDocumentPart(parent, in, call.AttachmentID, -1)
		if e != nil {
			return e
		}
		if part.Manifest.ImageProfile != chat.NarrowImageProfile || part.Manifest.Chunks != 0 {
			return chat.ErrInvalid
		}
		ref := llm.ImageReference{AttachmentID: call.AttachmentID, DerivedKey: chat.NarrowImageProfile, Checksum: in.Scope.ContentRef}
		req, e := ollama.ImageRequest(in.Model, call.Instruction, ref)
		if e != nil {
			return e
		}
		prior, e := w.WebToolResult(parent, in.Scope.ConversationID, in.Scope.ToolCallID)
		if e == nil {
			if prior.Analysis == nil || prior.Analysis.ImageProfile != chat.NarrowImageProfile {
				return chat.ErrInvalid
			}
			out = *prior.Analysis
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		ctx, cancel := context.WithTimeout(parent, 90*time.Second)
		defer cancel()
		raster, e := a.Attachments.ResolveImage(ctx, in.Scope, ref)
		if e != nil {
			return e
		}
		req.Image.PNG = raster.Data
		if a.Model == nil {
			return errors.New("model unavailable")
		}
		size := 0
		res, e := a.Model.Chat(ctx, req, func(delta llm.Delta) error {
			size += len(delta.Text)
			if size > 16000 {
				return chat.ErrInvalid
			}
			return ctx.Err()
		})
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if strings.TrimSpace(res.Message.Content) == "" || !utf8.ValidString(res.Message.Content) || len(res.Message.Content) > 16000 || len(res.Message.ToolCalls) > 0 {
			return chat.ErrInvalid
		}
		out = part.Manifest
		out.Truncated = res.Tokens >= 2000
		if out.Truncated {
			out.Status = "partial"
		}
		if e = w.SaveWebToolResult(ctx, in.Scope, id, chat.WebToolResult{AttachmentID: call.AttachmentID, Text: res.Message.Content, Analysis: &out, Truncated: out.Truncated, ExtractionStatus: out.Status}); e != nil {
			return e
		}
		// An older still-running attempt may have won. Adopt only its immutable SQL
		// result, never report the losing provider response as canonical completion.
		prior, e = w.WebToolResult(ctx, in.Scope.ConversationID, in.Scope.ToolCallID)
		if e == nil {
			out = *prior.Analysis
		}
		return e
	})
	return
}
