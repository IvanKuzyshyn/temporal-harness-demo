package tools

import (
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

type AttachmentAnalysisArgs struct {
	AttachmentID chat.AttachmentID `json:"attachmentId"`
	Instruction  string            `json:"instruction"`
}

// Resolve only persisted canonical arguments; callers must separately establish
// database authorization before reading bytes. This checks instruction identity,
// not display metadata or a caller-supplied filename.
func ValidateAttachmentAnalysis(s chat.ApprovalScope, args json.RawMessage) (AttachmentAnalysisArgs, error) {
	var out AttachmentAnalysisArgs
	if s.ToolName != "file_upload" || s.ToolVersion != "1" || s.ToolsetVersion != chat.WebToolset {
		return out, chat.ErrInvalid
	}
	call, err := NewWebRegistry(nil).Canonicalize("file_upload", args, s.ContentRef)
	if err != nil {
		return out, err
	}
	call.ID = s.ToolCallID
	raw, _ := json.Marshal(struct {
		Conversation chat.ConversationID
		Turn         chat.TurnID
		Set          string
		Call         chat.WebModelCall
	}{s.ConversationID, s.TurnID, chat.WebToolset, call})
	if WebDigest(raw) != s.ActionHash {
		return out, chat.ErrInvalid
	}
	err = json.Unmarshal(call.Arguments, &out)
	return out, err
}
