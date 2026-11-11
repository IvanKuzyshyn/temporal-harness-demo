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
	CommandID                        CommandID
	RequestID                        string
	ConversationID                   ConversationID
	TurnID                           TurnID
	WorkflowID, Model, PayloadDigest string
	Kind                             CommandKind
	Sequence                         int64
}

func (c SessionCommand) Identity() CommandIdentity {
	return CommandIdentity{CommandID: c.CommandID, RequestID: c.RequestID, ConversationID: c.ConversationID, TurnID: c.TurnID, WorkflowID: c.WorkflowID, Model: c.Model, PayloadDigest: c.PayloadDigest, Kind: c.Kind, Sequence: c.Sequence}
}

type AdmissionCheckpoint struct {
	Command  CommandIdentity
	Phase    string // check or receipt; receipt implies the enqueue decision is replayable
	Eligible bool
	Result   AdmissionResult
}

type AdmissionResult struct {
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
}
type Admission struct {
	Command SessionCommand
	Result  AdmissionResult
}

// TurnRef is safe to carry through Temporal; content stays in Postgres.
type TurnRef struct {
	ConversationID ConversationID
	TurnID         TurnID
}
type Turn struct {
	TurnRef
	Status, Reason, Text, Model string
	Sequence                    int64
}
type TurnResult struct {
	TurnRef
	Status, Reason string
}
type ChatModelResult struct {
	TurnRef
	Text           string
	Tokens         int
	AttemptID      string
	LimitedHistory bool
}
type Exchange struct {
	User, Assistant string
	Sequence        int64
}

// LogicalID is deterministic across activity retries AND workflow runs.
func LogicalID(turn TurnID, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(turn)+"/"+key)).String()
}
