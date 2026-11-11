package mock

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"testing"
)

func TestChatDeterministic(t *testing.T) {
	m := New()
	var streamed string
	res, err := m.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}}, func(d llm.Delta) error { streamed += d.Text; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if streamed == "" || res.Message.Content != streamed {
		t.Fatalf("stream=%q response=%+v", streamed, res)
	}
	again, err := m.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "hello"}}}, func(llm.Delta) error { return nil })
	if err != nil || again.Message.Content != streamed {
		t.Fatal("not deterministic")
	}
}
func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Chat(ctx, llm.Request{}, func(llm.Delta) error { return nil }); err == nil {
		t.Fatal("ignored cancellation")
	}
}
