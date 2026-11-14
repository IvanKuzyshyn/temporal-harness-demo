package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

func conversationJSON(c chat.Conversation) map[string]any {
	return map[string]any{"id": c.ID, "title": c.Title, "model": c.Model, "workflowId": c.WorkflowID, "lifecycle": c.Lifecycle, "policyRevision": c.PolicyRevision, "createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt}
}
func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		var in struct {
			RequestID     string  `json:"requestId"`
			Model         *string `json:"model"`
			PayloadDigest string  `json:"payloadDigest"`
		}
		if !decode(w, r, &in, "requestId", "model", "payloadDigest") {
			return
		}
		model := "qwen3.5:9b"
		if in.Model != nil {
			model = *in.Model
		}
		if !chat.ValidID(in.RequestID) || !chat.ValidModel(model) {
			fail(w, 400, "invalid_request")
			return
		}
		b, _ := json.Marshal(struct {
			Model string `json:"model"`
		}{model})
		h := sha256.Sum256(b)
		digest := hex.EncodeToString(h[:])
		if in.PayloadDigest != "" && in.PayloadDigest != digest {
			fail(w, 409, "conflict")
			return
		}
		if s.repo == nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		c, err := s.repo.CreateConversationOnce(r.Context(), in.RequestID, model)
		if err != nil {
			storageError(w, err)
			return
		}
		respond(w, 201, map[string]any{"conversation": conversationJSON(c), "payloadDigest": digest})
	case "GET":
		cursor, n, ok := pageArgs(w, r, "cursor")
		if !ok {
			return
		}
		if s.repo == nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		page, err := s.repo.ListConversations(r.Context(), cursor, n)
		if err != nil {
			storageError(w, err)
			return
		}
		items := []any{}
		for _, c := range page.Conversations {
			items = append(items, conversationJSON(c))
		}
		respond(w, 200, map[string]any{"conversations": items, "cursor": page.Cursor})
	default:
		fail(w, 405, "method_not_allowed")
	}
}
func (s *Server) metadata(w http.ResponseWriter, r *http.Request, id chat.ConversationID) {
	if s.repo == nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	p, err := s.repo.GetConversationProjection(r.Context(), id)
	if err != nil {
		storageError(w, err)
		return
	}
	respond(w, 200, metadataJSON(p, s.connected()))
}
func (s *Server) history(w http.ResponseWriter, r *http.Request, id chat.ConversationID) {
	before, n, ok := pageArgs(w, r, "before")
	if !ok {
		return
	}
	if s.repo == nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	if _, err := s.repo.GetConversation(r.Context(), id); err != nil {
		storageError(w, err)
		return
	}
	page, err := s.repo.ListMessages(r.Context(), id, before, n)
	if err != nil {
		storageError(w, err)
		return
	}
	items := []any{}
	for _, m := range page.Messages {
		items = append(items, messageJSON(m))
	}
	respond(w, 200, map[string]any{"messages": items, "before": page.Before})
}
