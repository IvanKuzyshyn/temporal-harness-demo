// Package workflows holds deterministic agent orchestration and wire contracts.
package workflows

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"time"
)

const (
	AgentsQueue            = "agents"
	LLMQueue               = "llm"
	SubagentsQueue         = "subagents"
	StateQuery             = "state"
	MessageUpdate          = "message"
	StopUpdate             = "stop"
	ContinueUpdate         = "continue"
	DecideUpdate           = "decide"
	SessionName            = "SessionWorkflow"
	ExplorerName           = "ExplorerWorkflow"
	LLMActivity            = "LLMStream"
	ToolActivity           = "ExecuteTool"
	EmitActivity           = "EmitEvent"
	TranscriptActivity     = "AppendTranscript"
	GatherActivity         = "GatherContext"
	NotifyActivity         = "NotifyApproval"
	CommandActivity        = "RunCommand"
	PreflightActivity      = "PreflightTool"
	RecoverCommandActivity = "RecoverCommand"
)

type Input struct {
	Model              string        `json:"model"`
	Workdir            string        `json:"workdir"`
	DataDir            string        `json:"dataDir"`
	MaxTurns           int           `json:"maxTurns"`
	ApprovalTimeout    time.Duration `json:"approvalTimeout"`
	HangTimeout        time.Duration `json:"hangTimeout"`
	ContinueAfterTurns int           `json:"continueAfterTurns"`
	Scenario           string        `json:"scenario,omitempty"`
	Carry              *State        `json:"carry,omitempty"`
}
type MessageInput struct {
	Text      string `json:"text"`
	WhenBusy  string `json:"whenBusy"`
	RequestID string `json:"requestId"`
	// Internal metadata is for workflow-owned follow-ups only. Updates reject it.
	Internal      bool   `json:"internal,omitempty"`
	OriginInputID string `json:"originInputId,omitempty"`
	ContextOnly   bool   `json:"contextOnly,omitempty"`
	// Snapshot of the origin's consumed restart budget (0..2) and verification obligation.
	AfterRunCount       int  `json:"afterRunCount,omitempty"`
	VerificationPending bool `json:"verificationPending,omitempty"`
}

// RetainedReceipt is a bounded FIFO retry window, carried across Continue-As-New.
type RetainedReceipt struct {
	RequestID string         `json:"requestId"`
	Digest    string         `json:"digest"`
	Receipt   MessageReceipt `json:"receipt"`
}
type MessageReceipt struct {
	Disposition string `json:"disposition"`
}
type DecisionInput struct {
	RequestID  string `json:"requestId"`
	ActionHash string `json:"actionHash"`
	Approved   bool   `json:"approved"`
	User       string `json:"user"`
	Reason     string `json:"reason"`
}
type PendingApproval struct {
	RequestID  string         `json:"requestId"`
	ActionHash string         `json:"actionHash"`
	Call       llm.ToolCall   `json:"call"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Decision   *DecisionInput `json:"decision,omitempty"`
	Preview    string         `json:"preview,omitempty"`
}
type Failure struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}
type ChildState struct {
	WorkflowID string `json:"workflowId"`
	Status     string `json:"status"`
	SummaryRef string `json:"summaryRef,omitempty"`
}
type State struct {
	Status               string            `json:"status"`
	Turn                 int               `json:"turn"`
	MaxTurns             int               `json:"maxTurns"`
	TranscriptRef        string            `json:"transcriptRef"`
	Inbox                []MessageInput    `json:"inbox"`
	PendingApproval      *PendingApproval  `json:"pendingApproval,omitempty"`
	ActiveTools          []llm.ToolCall    `json:"activeTools"`
	Children             []ChildState      `json:"children"`
	LastRunFailure       *Failure          `json:"lastRunFailure,omitempty"`
	LastSeq              uint64            `json:"lastSeq"`
	Answer               string            `json:"answer,omitempty"` // Bounded authoritative final answer, not the transcript.
	RunCounter           int               `json:"runCounter"`
	ApprovalCounter      int               `json:"approvalCounter"`
	ChildCounter         int               `json:"childCounter"`
	TotalTurns           int               `json:"totalTurns"`
	AfterRunCount        int               `json:"afterRunCount"`
	ContextGathered      bool              `json:"contextGathered"`
	WroteFile            bool              `json:"wroteFile"`
	VerificationPending  bool              `json:"verificationPending"` // Sticky for this origin until verification succeeds.
	HasUnansweredContext bool              `json:"hasUnansweredContext"`
	QueuePaused          bool              `json:"queuePaused"`
	ContinueRequested    bool              `json:"continueRequested"`
	OriginInputID        string            `json:"originInputId,omitempty"`
	Receipts             []RetainedReceipt `json:"receipts,omitempty"`
}
type LLMInput struct {
	// InputID is the stable logical run/input key, not an activity attempt ID.
	InputID       string
	Scope         events.Scope
	Model         string
	DataDir       string
	TranscriptRef string
	Turn          int
	Tools         []llm.ToolDefinition
	Scenario      string
}
type LLMResult struct {
	MessageRef string
	ToolCalls  []llm.ToolCall
	Summary    string
	Tokens     int
	LastSeq    uint64
}
type EmitInput struct {
	Scope   events.Scope
	Type    events.Type
	Pattern events.Pattern
	Data    any
	DataDir string
}
type AppendInput struct {
	// EntryID identifies this batch across retries; message indexes and roles
	// are included in the persisted entry keys.
	EntryID       string
	Scope         events.Scope
	DataDir       string
	TranscriptRef string
	Messages      []llm.Message
}
type AppendResult struct {
	TranscriptRef string
	LastSeq       uint64
}
type ToolInput struct {
	// InputID scopes model tool-call IDs to one logical run across activity retries.
	InputID     string
	Scope       events.Scope
	DataDir     string
	Workdir     string
	Call        llm.ToolCall
	HangTimeout time.Duration
}
type PreflightResult struct {
	Allowed          bool
	Reason           string
	RequiresApproval bool
	Mode             tools.ExecutionMode
	Replay           tools.Replay
	ActionHash       string
	Preview          string
}

type GatherInput struct {
	Scope   events.Scope
	DataDir string
	Workdir string
	Kind    string
}
type ExplorerInput struct {
	ParentScope events.Scope
	Config      Input
	Task        string
	Path        string
	MaxSteps    int
	TokenBudget int
}
type ExplorerResult struct {
	SummaryRef string `json:"summaryRef"`
	Summary    string `json:"summary"`
}
type CommandResult struct {
	ExitCode    int    `json:"exitCode"`
	Truncated   bool   `json:"truncated"`
	LogRef      string `json:"logRef"`
	Tail        string `json:"tail"`
	Interrupted bool   `json:"interrupted"`
}
type BatchResult struct {
	Results []tools.Result
	LastSeq uint64
}
