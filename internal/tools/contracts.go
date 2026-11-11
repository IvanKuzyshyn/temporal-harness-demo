// Package tools defines metadata, policy and bounded project-local tools.
package tools

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

type ExecutionMode string
type Replay string

const (
	Parallel   ExecutionMode = "parallel"
	Sequential ExecutionMode = "sequential"
	Safe       Replay        = "safe"
	Unsafe     Replay        = "unsafe"
)

type Definition struct {
	llm.ToolDefinition
	ExecutionMode ExecutionMode
	Replay        Replay
	Callers       []string
	NeedsApproval func(json.RawMessage) bool
}
type Result struct {
	ToolCallID  string            `json:"toolCallId"`
	Output      string            `json:"output"`
	Structured  json.RawMessage   `json:"structured,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
	IsError     bool              `json:"isError"`
	Interrupted bool              `json:"interrupted,omitempty"`
}
type Registry interface {
	Lookup(string) (Definition, bool)
	Definitions(caller string) []Definition
	Execute(context.Context, llm.ToolCall) Result
}
type FileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}
type GrepArgs struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}
type TreeArgs struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}
type CommandArgs struct {
	Cmd     string   `json:"cmd"`
	Args    []string `json:"args"`
	Workdir string   `json:"workdir,omitempty"`
}
type DelegateArgs struct {
	Path     string `json:"path"`
	Question string `json:"question"`
}
