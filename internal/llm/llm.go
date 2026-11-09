// Package llm is the model boundary. Implementations stream deltas and return a final message.
package llm

import (
	"context"
	"encoding/json"
)

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ToolName   string     `json:"toolName,omitempty"`
}
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolDefinition
	Scenario string
	Turn     int
}
type Delta struct {
	Text   string
	Tokens int
}
type Response struct {
	Message Message
	Tokens  int
}
type Model interface {
	Chat(context.Context, Request, func(Delta) error) (Response, error)
}
