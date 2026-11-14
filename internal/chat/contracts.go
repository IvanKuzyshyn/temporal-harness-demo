// Package chat defines storage-facing identities and intent contracts. Receipts
// acknowledge durable intent, never tool authorization or workflow completion.
package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strings"
	"time"
)

type ConversationID string
type TurnID string
type AttachmentID string
type CommandID string
type CommandKind string
type ApprovalDecision string

const (
	CommandMessage CommandKind      = "message"
	CommandReplace CommandKind      = "replace"
	CommandStop    CommandKind      = "stop"
	CommandDecide  CommandKind      = "decide"
	CommandRevoke  CommandKind      = "revoke"
	ApprovalOnce   ApprovalDecision = "once"
	ApprovalAll    ApprovalDecision = "all"
	ApprovalDeny   ApprovalDecision = "deny"
)

var ErrInvalid = errors.New("invalid chat input")

// ValidID requires the canonical UUID spelling so JSON digests and SQL UUID
// equality agree. UUID identities are opaque; no caller-controlled SQL names.
func ValidID(s string) bool {
	u, e := uuid.Parse(s)
	return e == nil && u != uuid.Nil && u.String() == s
}

// EnabledModels returns a fresh catalog so callers cannot mutate admission.
// Provider routing remains model-driven; adding a model requires explicit
// qualification, profile/pin and database admission changes.
func EnabledModels() []string { return []string{"qwen3.5:9b"} }
func ValidModel(s string) bool {
	for _, model := range EnabledModels() {
		if s == model {
			return true
		}
	}
	return false
}

const MaxAttachments = 1

func ValidHash(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}

type Conversation struct {
	ID                                  ConversationID
	Title, Model, WorkflowID, Lifecycle string
	PolicyRevision                      int64
	CreatedAt, UpdatedAt                time.Time
}
type CommandInput struct {
	// Request IDs deduplicate across kinds within ONE conversation, indefinitely.
	RequestID      string
	ConversationID ConversationID
	PayloadDigest  string // optional assertion; always recomputed server-side
	Kind           CommandKind
	Payload        CommandPayload
}

// CommandPayload is a validated discriminated union selected by CommandKind.
// Irrelevant fields are rejected, not silently omitted from the digest.
type CommandPayload struct {
	Text                 string           `json:"text,omitempty"`
	AttachmentIDs        []AttachmentID   `json:"attachmentIds,omitempty"`
	ExpectedActiveTurnID TurnID           `json:"expectedActiveTurnId,omitempty"`
	ApprovalID           string           `json:"approvalId,omitempty"`
	ActionHash           string           `json:"actionHash,omitempty"`
	Decision             ApprovalDecision `json:"decision,omitempty"`
}

func (in CommandInput) Digest() (string, error) {
	if !ValidID(in.RequestID) || !ValidID(string(in.ConversationID)) {
		return "", ErrInvalid
	}
	p := in.Payload
	hasMessage := p.Text != "" || len(p.AttachmentIDs) > 0
	hasDecision := p.ApprovalID != "" || p.ActionHash != "" || p.Decision != ""
	if len(p.AttachmentIDs) > MaxAttachments {
		return "", ErrInvalid
	}
	seen := map[AttachmentID]bool{}
	for _, id := range p.AttachmentIDs {
		if !ValidID(string(id)) || seen[id] {
			return "", ErrInvalid
		}
		seen[id] = true
	}
	if p.ExpectedActiveTurnID != "" && !ValidID(string(p.ExpectedActiveTurnID)) {
		return "", ErrInvalid
	}
	switch in.Kind {
	case CommandMessage:
		if !hasMessage || p.ExpectedActiveTurnID != "" || hasDecision {
			return "", ErrInvalid
		}
	case CommandReplace:
		if !hasMessage || p.ExpectedActiveTurnID == "" || hasDecision {
			return "", ErrInvalid
		}
	case CommandStop:
		if hasMessage || p.ExpectedActiveTurnID == "" || hasDecision {
			return "", ErrInvalid
		}
	case CommandDecide:
		if hasMessage || p.ExpectedActiveTurnID != "" || !ValidID(p.ApprovalID) || !ValidHash(p.ActionHash) || (p.Decision != ApprovalOnce && p.Decision != ApprovalAll && p.Decision != ApprovalDeny) {
			return "", ErrInvalid
		}
	case CommandRevoke:
		if hasMessage || p.ExpectedActiveTurnID != "" || hasDecision {
			return "", ErrInvalid
		}
	default:
		return "", ErrInvalid
	}
	b, err := json.Marshal(struct {
		Kind    CommandKind    `json:"kind"`
		Payload CommandPayload `json:"payload"`
	}{in.Kind, p})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

type CommandReceipt struct {
	CommandID                                 CommandID
	TurnID                                    TurnID
	PayloadDigest, DispatchState, ResultState string
	Result                                    json.RawMessage
}
type StoredCommand struct {
	Input   CommandInput
	Receipt CommandReceipt
}
type MessageRecord struct {
	TurnStatus                  string           `json:",omitempty"` // read-only projection; excluded from identity
	TurnReason                  string           `json:",omitempty"`
	Context                     *ContextMetadata `json:",omitempty"` // read-only final-result metadata
	EntryID                     string
	ConversationID              ConversationID
	TurnID                      TurnID
	Role, Text                  string
	AttachmentIDs               []AttachmentID
	LogicalStep                 int
	ResultKind, CompletionState string
	Sequence                    int64     // DB-assigned; excluded from write identity
	CreatedAt                   time.Time // DB-assigned
}
type MessagePage struct {
	Messages []MessageRecord
	Before   string
}
type Attachment struct {
	ID                           AttachmentID
	ConversationID               ConversationID
	OriginalName, MediaType      string
	Size                         int64
	Checksum, StorageKey, Status string
	ExtractedText                string
	DerivedImageKeys             []string
}
type ToolCall struct {
	ID                                                    string
	ConversationID                                        ConversationID
	TurnID                                                TurnID
	LogicalKey, Name                                      string
	Arguments                                             json.RawMessage
	ActionHash, ParentWorkflowID, ChildWorkflowID, Status string
	Result, Sources                                       json.RawMessage
}
type Approval struct {
	ID                     string
	ConversationID         ConversationID
	ToolCallID, ActionHash string
	Deadline               time.Time
	Decision               ApprovalDecision
	PolicyRevision         int64
	DecidedAt              *time.Time
}
type Summary struct {
	ID                       string
	ConversationID           ConversationID
	Version, CoveredSequence int64
	Text, Model              string
	Budget                   json.RawMessage
}
type Event struct {
	ID                   string
	ConversationID       ConversationID
	TurnID               TurnID
	LogicalStep, Attempt int // both absent when Attempt == 0
	Type                 string
	Payload              json.RawMessage // lifecycle data only; never original file/image bytes
	Sequence             int64
}
type ModelAttempt struct {
	ID                   string
	ConversationID       ConversationID
	TurnID               TurnID
	LogicalStep, Attempt int
	Status, Error        string
	Usage                json.RawMessage
	ResultEntryID        string
}
