package chat

import "github.com/google/uuid"

// SessionCommand is the persisted outbox envelope, not a second user append.
// CommandID is also the Temporal Update ID; Sequence is acceptance order only.
type SessionCommand struct {
	CommandID                        CommandID
	RequestID                        string
	ConversationID                   ConversationID
	TurnID                           TurnID
	WorkflowID, Model, PayloadDigest string
	Kind                             CommandKind
	Payload                          CommandPayload
	Sequence                         int64
}

// CommandIdentity is bounded admission carry. Payload was validated by the
// original Update; the digest and every identity are rechecked against SQL.
type CommandIdentity struct {
	ApprovalID, ActionHash           string
	Decision                         ApprovalDecision
	ExpectedActiveTurnID             TurnID
	CommandID                        CommandID
	RequestID                        string
	ConversationID                   ConversationID
	TurnID                           TurnID
	WorkflowID, Model, PayloadDigest string
	Kind                             CommandKind
	Sequence                         int64
}

func (c SessionCommand) Identity() CommandIdentity {
	return CommandIdentity{CommandID: c.CommandID, RequestID: c.RequestID, ConversationID: c.ConversationID, TurnID: c.TurnID, WorkflowID: c.WorkflowID, Model: c.Model, PayloadDigest: c.PayloadDigest, Kind: c.Kind, Sequence: c.Sequence, ExpectedActiveTurnID: c.Payload.ExpectedActiveTurnID, ApprovalID: c.Payload.ApprovalID, ActionHash: c.Payload.ActionHash, Decision: c.Payload.Decision}
}

type AdmissionCheckpoint struct {
	Command  CommandIdentity
	Phase    string // check or receipt; receipt implies the enqueue decision is replayable
	Eligible bool
	Result   AdmissionResult
}

type AdmissionResult struct {
	PolicyRevision    int64  `json:"policyRevision,omitempty"`
	GrantedToolset    string `json:"grantedToolset,omitempty"`
	State             string `json:"state"`
	Code              string `json:"code,omitempty"`
	TargetTurnID      TurnID `json:"targetTurnId,omitempty"`
	ReplacementTurnID TurnID `json:"replacementTurnId,omitempty"`
}

// ReplacementInput is a correcting prompt, not authority to read attachments.
type ReplacementInput struct {
	RequestID            string
	ExpectedActiveTurnID TurnID
	Text                 string
	AttachmentIDs        []AttachmentID
}
type Admission struct {
	Command SessionCommand
	Result  AdmissionResult
}

// TurnRef is safe to carry through Temporal; content stays in Postgres.
type TurnRef struct {
	Step           int `json:",omitempty"`
	ConversationID ConversationID
	TurnID         TurnID
}
type Turn struct {
	TurnRef
	Status, Reason, Text, Model string
	Sequence                    int64
	AttachmentIDs               []AttachmentID
	ReplacementTarget           TurnID
	OriginalText                string
	OriginalAttachmentIDs       []AttachmentID
}
type TurnResult struct {
	TurnRef
	Status, Reason string
}
type ChatModelResult struct {
	Calls []WebModelCall `json:",omitempty"`
	TurnRef
	Text           string
	Tokens         int
	AttemptID      string
	LimitedHistory bool
	Context        *ContextMetadata `json:",omitempty"`
}
type Exchange struct {
	User, Assistant string
	Sequence        int64 // assistant completion sequence in context snapshots
	Results         []WebToolResult
	OmittedResults  int
}

// LogicalID is deterministic across activity retries AND workflow runs.
func LogicalID(turn TurnID, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(turn)+"/"+key)).String()
}
