package chat

import (
	"encoding/json"
	"time"
)

// WebToolset is deliberately closed. Changing either registered tool version
// requires a new set version; an old conversation grant never expands itself.
const WebToolset = "web-v1: web_fetch@1,file_upload@1"
const MaxWebSteps = 12

type ApprovalScope struct {
	ConversationID ConversationID
	TurnID         TurnID
	ToolCallID     string
	ToolsetVersion string
	ToolName       string
	ToolVersion    string
	ActionHash     string
	ContentRef     string // immutable URL/attachment digest, never original bytes
	PolicyRevision int64
}
type DecideInput struct {
	RequestID, ApprovalID, ActionHash string
	Decision                          ApprovalDecision
}
type WebPolicy struct {
	Revision       int64
	GrantedToolset string
}
type WebAction struct {
	Scope    ApprovalScope
	Outcome  string // empty, authorized, denied, expired, cancelled
	Recorded bool
}
type WebApproval struct {
	ID       string
	Scope    ApprovalScope
	Deadline time.Time
	Decision ApprovalDecision
}
type ModelStepResult struct{ Actions []WebAction }
type WebModelCall struct {
	ID, Name               string
	Arguments              json.RawMessage
	ActionHash, ContentRef string
}
type WebApprovalRecord struct {
	Approval WebApproval
	Outcome  string
}
type WebToolExecution struct{ Scope ApprovalScope }
type WebToolResult struct {
	AttachmentID     AttachmentID `json:"attachmentId,omitempty"`
	Pages            int          `json:"pages,omitempty"`
	ExtractionStatus string       `json:"extractionStatus,omitempty"`
	Text             string       `json:"text"`
	Error            string       `json:"error,omitempty"`
	RequestedURL     string       `json:"requestedUrl,omitempty"`
	FinalURL         string       `json:"finalUrl,omitempty"`
	ContentType      string       `json:"contentType,omitempty"`
	Truncated        bool         `json:"truncated"`
}
