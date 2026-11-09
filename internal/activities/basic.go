// Package activities is the I/O boundary for workflow orchestration.
package activities

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

const MaxSummaryBytes = 8192

type Activities struct{ Model llm.Model }

func bounded(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// emitDisplay preserves only successful sequence numbers. Stream failures never
// affect durable work; cancellation still propagates to the caller.
func (a *Activities) emitDisplay(ctx context.Context, in workflows.EmitInput, last *uint64) error {
	seq, err := a.EmitEvent(ctx, in)
	if err != nil {
		slog.WarnContext(ctx, "Display emission failed", "type", in.Type, "error", err)
	} else if seq > *last {
		*last = seq
	}
	return ctx.Err()
}

func (a *Activities) EmitEvent(ctx context.Context, in workflows.EmitInput) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	event, err := events.NewStore(in.DataDir).Append(in.Scope, in.Type, in.Pattern, in.Data)
	return event.Seq, err
}
