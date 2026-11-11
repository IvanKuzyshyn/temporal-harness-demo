package api

import (
	"net/http"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

func receiptJSON(a chat.CommandReceipt) map[string]any {
	var turn any
	if a.TurnID != "" {
		turn = a.TurnID
	}
	return map[string]any{"commandId": a.CommandID, "turnId": turn, "payloadDigest": a.PayloadDigest, "dispatchState": a.DispatchState, "resultState": a.ResultState, "result": a.Result}
}
func (s *Server) command(w http.ResponseWriter, r *http.Request, id chat.ConversationID, path []string) {
	var kind chat.CommandKind
	fields := []string{"requestId", "payloadDigest"}
	approvalID := ""
	switch strings.Join(path, "/") {
	case "messages":
		kind = chat.CommandMessage
		fields = append(fields, "text", "attachmentIds")
	case "replace":
		kind = chat.CommandReplace
		fields = append(fields, "expectedActiveTurnId", "text", "attachmentIds")
	case "stop":
		kind = chat.CommandStop
		fields = append(fields, "expectedActiveTurnId")
	case "approval-policy/revoke":
		kind = chat.CommandRevoke
	default:
		if len(path) != 3 || path[0] != "approvals" || path[2] != "decision" {
			fail(w, 404, "not_found")
			return
		}
		approvalID = path[1]
		kind = chat.CommandDecide
		fields = append(fields, "actionHash", "decision")
	}
	var in struct {
		RequestID            string                `json:"requestId"`
		PayloadDigest        string                `json:"payloadDigest"`
		Text                 string                `json:"text"`
		AttachmentIDs        []chat.AttachmentID   `json:"attachmentIds"`
		ExpectedActiveTurnID chat.TurnID           `json:"expectedActiveTurnId"`
		ActionHash           string                `json:"actionHash"`
		Decision             chat.ApprovalDecision `json:"decision"`
	}
	if !decode(w, r, &in, fields...) {
		return
	}
	if len(in.Text) > 65536 {
		fail(w, 413, "prompt_too_large")
		return
	}
	command := chat.CommandInput{RequestID: in.RequestID, ConversationID: id, PayloadDigest: in.PayloadDigest, Kind: kind, Payload: chat.CommandPayload{Text: in.Text, AttachmentIDs: in.AttachmentIDs, ExpectedActiveTurnID: in.ExpectedActiveTurnID, ApprovalID: approvalID, ActionHash: in.ActionHash, Decision: in.Decision}}
	if _, err := command.Digest(); err != nil {
		storageError(w, err)
		return
	}
	if s.repo == nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	a, err := s.repo.AcceptCommand(r.Context(), command)
	if err != nil {
		storageError(w, err)
		return
	}
	w.Header().Set("Location", "/conversations/"+string(id)+"/commands/"+string(a.CommandID))
	respond(w, 202, receiptJSON(a))
}
