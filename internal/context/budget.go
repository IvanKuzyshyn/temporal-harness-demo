package context

import (
	"fmt"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
)

type Budget struct{ Context, Input, Output, Reserve, Recent, Summary, Excerpts, QualifiedImageReserve int }

func DefaultBudget() Budget { return Budget{16384, 12000, 2000, 2384, 6, 1500, 4000, 0} }
func (b Budget) valid() bool {
	return b.Context > 0 && b.Context <= 16384 && b.Input > 0 && b.Input <= 12000 && b.Output > 0 && b.Output <= 2000 && b.Reserve >= 2384 && b.Recent >= 0 && b.Recent <= 6 && b.Summary > 0 && b.Summary <= 1500 && b.Excerpts > 0 && b.Excerpts <= 4000 && b.QualifiedImageReserve >= 0
}
func invalid(s string) error { return &ollama.InvalidRequestError{Err: fmt.Errorf("context: %s", s)} }
func account(r llm.Request, b Budget) (chat.ContextAccounting, error) {
	a := chat.ContextAccounting{Method: "conservative source-bound estimate; not measured tokens", OutputTokens: r.MaxOutputTokens, Reserve: b.Reserve}
	if !b.valid() || r.ContextWindow != b.Context || r.MaxOutputTokens != b.Output {
		return a, invalid("invalid budget")
	}
	var err error
	if r.Image != nil {
		a.InputTokens, a.ImageTokens, err = ollama.ImageInputBound(r)
		if b.QualifiedImageReserve > 0 && b.QualifiedImageReserve < a.ImageTokens {
			return a, invalid("image reservation too small")
		}
		if b.QualifiedImageReserve > a.ImageTokens {
			a.ImageTokens = b.QualifiedImageReserve
		}
	} else {
		a.InputTokens, err = ollama.TextInputBound(r)
	}
	a.Total = a.InputTokens + a.ImageTokens + a.OutputTokens + a.Reserve
	if err == nil && (a.InputTokens > b.Input || a.Total > b.Context) {
		err = invalid("input too large; mandatory inputs are not truncated")
	}
	return a, err
}
