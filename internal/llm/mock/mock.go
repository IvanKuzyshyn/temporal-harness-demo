// Package mock supplies deterministic text responses for isolated web tests.
package mock

import (
	"context"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

type Model struct{}

func New() *Model { return &Model{} }

func (*Model) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	text := "Mock assistant: ready."
	res := llm.Response{Message: llm.Message{Role: "assistant", Content: text}}
	for _, word := range strings.SplitAfter(text, " ") {
		if err := ctx.Err(); err != nil {
			return llm.Response{}, err
		}
		if emit != nil {
			if err := emit(llm.Delta{Text: word, Tokens: 1}); err != nil {
				return llm.Response{}, err
			}
		}
		res.Tokens++
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return res, nil
}

var _ llm.Model = (*Model)(nil)
