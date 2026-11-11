// Package mock supplies deterministic local model responses without network access.
package mock

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

type Model struct{}

func New() *Model { return &Model{} }
func (*Model) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	text := "Mock assistant: ready."
	if err := emit(llm.Delta{Text: text, Tokens: 4}); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: text}, Tokens: 4}, nil
}

var _ llm.Model = (*Model)(nil)
