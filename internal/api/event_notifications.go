package api

import (
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"strconv"
)

// These lifecycle notifications are changes to durable SQL projections, not
// invented execution receipts. Their watermark is shared with the accompanying
// snapshot. Replay need not retain them: the snapshot reconstructs all fields.
func snapshotNotifications(id chat.ConversationID, s storage.EventSnapshot) map[string]eventEnvelope {
	out := map[string]eventEnvelope{}
	add := func(key, kind string, turn any, data any) {
		out[key] = eventEnvelope{ID: strconv.FormatInt(s.Cursor, 10), ConversationID: id, TurnID: turn, Type: kind, Data: data}
	}
	for _, m := range s.History.Messages {
		add("message/"+m.EntryID, "message.persisted", m.TurnID, messageJSON(m))
	}
	for _, group := range []struct {
		raw       json.RawMessage
		kind, key string
	}{{s.Commands, "command.result", "commandId"}, {s.Turns, "turn.status", "id"}, {s.Tools, "tool.state", "id"}} {
		var rows []map[string]any
		if json.Unmarshal(group.raw, &rows) != nil {
			continue
		}
		for _, r := range rows {
			key, _ := r[group.key].(string)
			turn := r["turnId"]
			if group.kind == "turn.status" {
				turn = r["id"]
			}
			add(group.kind+"/"+key, group.kind, turn, r)
		}
	}
	add("approval", "approval.state", nil, s.Projection.PendingApproval)
	add("policy", "policy.state", nil, map[string]any{"revision": s.Projection.Conversation.PolicyRevision, "approvalAll": s.Projection.ApprovalToolset == chat.WebToolset, "toolsetVersion": s.Projection.ApprovalToolset})
	return out
}
