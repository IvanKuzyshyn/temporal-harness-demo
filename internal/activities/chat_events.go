package activities

import (
	"context"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"sync"
	"time"
	"unicode/utf8"
)

type displayWriter interface {
	AppendEvent(context.Context, chat.Event) error
}

// A single coalesced draft, never a token queue. SQL/display failure is lossy,
// not execution failure. Each body joins its bounded publisher before cleanup.
// Cumulative text tolerates dropped batches. Canonical messages never use this.
func chatDeltas(ctx context.Context, repo ChatRepository, ref chat.TurnRef, attempt int) (func(string), func()) {
	writer, ok := repo.(displayWriter)
	if !ok {
		return func(string) {}, func() {}
	}
	var mu sync.Mutex
	text := ""
	bounded := false
	version := 0
	sent := -1
	done := make(chan struct{})
	joined := make(chan struct{})
	publish := func() {
		mu.Lock()
		v, t, b := version, text, bounded
		mu.Unlock()
		if v == sent {
			return
		}
		sent = v
		raw, _ := json.Marshal(map[string]any{"text": t, "bounded": b})
		c, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		_ = writer.AppendEvent(c, chat.Event{ID: uuid.NewString(), ConversationID: ref.ConversationID, TurnID: ref.TurnID, LogicalStep: ref.Step, Attempt: attempt, Type: "message.delta", Payload: raw})
	}
	go func() {
		defer close(joined)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				publish()
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()
	return func(delta string) {
		mu.Lock()
		defer mu.Unlock()
		if bounded {
			return
		}
		text += delta
		if len(text) >= 8000 {
			bounded = true
			text = text[:8000]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		version++
	}, func() { close(done); <-joined }
}
