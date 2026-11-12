// Package llm is the model boundary. Implementations stream deltas and return a final message.
package llm

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
)

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ImageReference is safe for durable history. It never carries original bytes.
type ImageReference struct {
	AttachmentID         chat.AttachmentID
	DerivedKey, Checksum string
}

// NarrowImageProfile is a separate source-bounded profile, not global image
// qualification. Bytes are activity-local and explicitly excluded from JSON.
const NarrowImageProfile = chat.NarrowImageProfile

type ResolvedImage struct {
	Profile   string
	Reference ImageReference
	PNG       []byte
}

type Message struct {
	Images     []ImageReference `json:"images,omitempty"`
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []ToolCall       `json:"toolCalls,omitempty"`
	ToolCallID string           `json:"toolCallId,omitempty"`
	ToolName   string           `json:"toolName,omitempty"`
}
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type Request struct {
	Image *ResolvedImage `json:"-"`
	// Zero preserves legacy provider defaults. Web chat always sets explicit bounds.
	ContextWindow, MaxOutputTokens, MaxResponseBytes int
	Model                                            string
	Messages                                         []Message
	Tools                                            []ToolDefinition
	Scenario                                         string
	Turn                                             int
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
