// Package events defines the display-only JSONL protocol. Workflow state is authoritative.
package events

import "encoding/json"

type Type string
type Pattern string

const (
	None              Pattern = "none"
	Parallel          Pattern = "parallel"
	Child             Pattern = "child"
	LongRunning       Pattern = "long_running"
	Approval          Pattern = "approval"
	AgentStart        Type    = "agent_start"
	TurnStart         Type    = "turn_start"
	TurnEnd           Type    = "turn_end"
	MessageStart      Type    = "message_start"
	MessageEnd        Type    = "message_end"
	MessageUpdate     Type    = "message_update"
	ToolStart         Type    = "tool_execution_start"
	ToolUpdate        Type    = "tool_execution_update"
	ToolEnd           Type    = "tool_execution_end"
	ApprovalRequested Type    = "approval_requested"
	ApprovalResolved  Type    = "approval_resolved"
	ChildStarted      Type    = "child_started"
	ChildCompleted    Type    = "child_completed"
	ParallelStart     Type    = "parallel_batch_start"
	ParallelEnd       Type    = "parallel_batch_end"
	Heartbeat         Type    = "heartbeat"
	AgentEnd          Type    = "agent_end"
	AgentSettled      Type    = "agent_settled"
	RunFailed         Type    = "run_failed"
)

var Types = []Type{AgentStart, TurnStart, TurnEnd, MessageStart, MessageEnd, MessageUpdate, ToolStart, ToolUpdate, ToolEnd, ApprovalRequested, ApprovalResolved, ChildStarted, ChildCompleted, ParallelStart, ParallelEnd, Heartbeat, AgentEnd, AgentSettled, RunFailed}

type Event struct {
	Seq              uint64          `json:"seq"`
	TS               string          `json:"ts"`
	WorkflowID       string          `json:"workflowId"`
	RunID            string          `json:"runId"`
	ParentWorkflowID string          `json:"parentWorkflowId"`
	Type             Type            `json:"type"`
	Pattern          Pattern         `json:"pattern"`
	Data             json.RawMessage `json:"data"`
}

// Scope identifies the emitting workflow and the stream receiving the event.
// StreamID is the parent's ID for explorer events.
type Scope struct {
	StreamID         string `json:"streamId"`
	WorkflowID       string `json:"workflowId"`
	RunID            string `json:"runId"`
	ParentWorkflowID string `json:"parentWorkflowId,omitempty"`
}

type AgentData struct {
	InputID    string `json:"inputId,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Status     string `json:"status,omitempty"`
	RunFailure string `json:"runFailure,omitempty"`
}
type TurnData struct {
	Turn        int      `json:"turn"`
	ToolResults []string `json:"toolResults,omitempty"`
}
type MessageData struct {
	Role       string `json:"role,omitempty"`
	MessageRef string `json:"messageRef,omitempty"`
	Delta      string `json:"delta,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
}
type ToolData struct {
	ToolCallID    string            `json:"toolCallId"`
	Name          string            `json:"name,omitempty"`
	Args          json.RawMessage   `json:"args,omitempty"`
	ExecutionMode string            `json:"executionMode,omitempty"`
	Details       map[string]string `json:"details,omitempty"`
	Chunk         string            `json:"chunk,omitempty"`
	Offset        int64             `json:"offset,omitempty"`
	IsError       bool              `json:"isError,omitempty"`
	Interrupted   bool              `json:"interrupted,omitempty"`
	Summary       string            `json:"summary,omitempty"`
}
type ApprovalData struct {
	RequestID  string `json:"requestId"`
	ActionHash string `json:"actionHash"`
	Command    string `json:"command,omitempty"`
	Decision   string `json:"decision,omitempty"`
	User       string `json:"user,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
type ChildData struct {
	ChildWorkflowID string `json:"childWorkflowId"`
	Task            string `json:"task,omitempty"`
	SummaryRef      string `json:"summaryRef,omitempty"`
}
type ParallelData struct {
	ToolCallIDs []string `json:"toolCallIds"`
	Concurrency int      `json:"concurrency"`
}
type HeartbeatData struct {
	Activity string `json:"activity"`
	Progress int64  `json:"progress"`
}
