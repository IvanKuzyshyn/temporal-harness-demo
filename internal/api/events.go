package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"net/http"
	"strconv"
	"time"
)

type eventEnvelope struct {
	ID             string              `json:"id"`
	ConversationID chat.ConversationID `json:"conversationId"`
	TurnID         any                 `json:"turnId"`
	StepID         any                 `json:"stepId"`
	Attempt        any                 `json:"attempt"`
	Type           string              `json:"type"`
	Data           any                 `json:"data"`
}

func metadataJSON(p storage.ConversationProjection, connected bool) map[string]any {
	return map[string]any{"conversation": conversationJSON(p.Conversation), "activeTurn": p.ActiveTurn, "pendingApproval": p.PendingApproval, "policy": map[string]any{"revision": p.Conversation.PolicyRevision, "approvalAll": p.ApprovalToolset == chat.WebToolset, "toolsetVersion": p.ApprovalToolset}, "freshness": map[string]any{"source": "postgres_projection", "authoritative": false, "observedAt": time.Now().UTC(), "workflowConfirmedAt": nil}, "dispatcherConnected": connected}
}
func messageJSON(m chat.MessageRecord) map[string]any {
	return map[string]any{"entryId": m.EntryID, "conversationId": m.ConversationID, "turnId": m.TurnID, "role": m.Role, "text": m.Text, "attachmentIds": m.AttachmentIDs, "logicalStep": m.LogicalStep, "resultKind": m.ResultKind, "completionState": m.CompletionState, "context": m.Context, "turnStatus": m.TurnStatus, "turnReason": m.TurnReason, "sequence": m.Sequence, "createdAt": m.CreatedAt}
}
func (s *Server) events(w http.ResponseWriter, r *http.Request, id chat.ConversationID) {
	if r.Method != "GET" {
		fail(w, 405, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || len(r.Header.Values("Last-Event-ID")) > 1 {
		fail(w, 400, "invalid_query")
		return
	}
	cursor := int64(0)
	raw := r.Header.Get("Last-Event-ID")
	if raw != "" {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < 0 || strconv.FormatInt(n, 10) != raw {
			fail(w, 400, "invalid_query")
			return
		}
		cursor = n
	}
	if s.repo == nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	load := func() (storage.EventSnapshot, error) {
		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		defer cancel()
		return s.repo.EventSnapshot(ctx, id)
	}
	snap, err := load()
	if err != nil {
		storageError(w, err)
		return
	}
	reset := raw == ""
	replay := false
	if raw != "" {
		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		replay, err = s.repo.CanResumeEvents(ctx, id, cursor)
		cancel()
		if err != nil {
			storageError(w, err)
			return
		}
		reset = !replay
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	// Each write has its own bound. Never use the ordinary 5s request timeout for
	// an SSE lifetime, nor the server's absolute 15s write timeout.
	write := func(e eventEnvelope) bool {
		if rc.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return false
		}
		b, err := json.Marshal(e)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", e.ID, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	previous := map[string][32]byte(nil)
	sendSnapshot := func(v storage.EventSnapshot, reset bool) bool {
		changes := snapshotNotifications(id, v)
		current := make(map[string][32]byte, len(changes))
		for key, event := range changes {
			raw, _ := json.Marshal(event.Data)
			hash := sha256.Sum256(raw)
			current[key] = hash
			if previous != nil && previous[key] != hash && !write(event) {
				return false
			}
		}
		previous = current
		messages := []any{}
		for _, m := range v.History.Messages {
			messages = append(messages, messageJSON(m))
		}
		return write(eventEnvelope{ID: strconv.FormatInt(v.Cursor, 10), ConversationID: id, Type: "snapshot", Data: map[string]any{"reset": reset, "metadata": metadataJSON(v.Projection, s.connected()), "messages": messages, "before": v.History.Before, "turns": v.Turns, "commands": v.Commands, "tools": v.Tools, "attempts": v.Attempts}})
	}
	sendEvents := func() bool {
		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		defer cancel()
		events, e := s.repo.ListEvents(ctx, id, cursor, 64)
		if e != nil {
			return false
		}
		for _, v := range events {
			ev := eventEnvelope{ID: strconv.FormatInt(v.Sequence, 10), ConversationID: id, Type: v.Type, Data: v.Payload}
			if v.TurnID != "" {
				ev.TurnID = v.TurnID
			}
			if v.Attempt > 0 {
				ev.StepID = v.LogicalStep
				ev.Attempt = v.Attempt
			}
			if !write(ev) {
				return false
			}
			cursor = v.Sequence
		}
		return true
	}
	if replay {
		for cursor < snap.Cursor {
			previous := cursor
			if !sendEvents() {
				return
			}
			if cursor == previous {
				reset = true
				break
			}
		}
	}
	// Fresh snapshot after replay fences every projection and event watermark.
	snap, err = load()
	if err != nil {
		return
	}
	if !sendSnapshot(snap, reset) {
		return
	}
	cursor = snap.Cursor
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	count := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if !sendEvents() {
				return
			}
			count++
			if count%4 == 0 {
				snap, err = load()
				if err != nil {
					return
				}
				if !sendSnapshot(snap, false) {
					return
				}
				cursor = snap.Cursor
			}
		}
	}
}
