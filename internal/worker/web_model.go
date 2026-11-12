package worker

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

// One process-wide generation per web runner, including document and chat
// activities. Waiting bodies remain heartbeat/cancellation responsive.
type serialWebModel struct {
	model llm.Model
	gate  chan struct{}
}

func (m *serialWebModel) Chat(ctx context.Context, in llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	select {
	case m.gate <- struct{}{}:
		defer func() { <-m.gate }()
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return m.model.Chat(ctx, in, emit)
}
