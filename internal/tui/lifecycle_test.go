package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type lifetimeBackend struct {
	Backend
	started chan context.Context
}

func (b *lifetimeBackend) wait(ctx context.Context) error {
	b.started <- ctx
	<-ctx.Done()
	return ctx.Err()
}
func (b *lifetimeBackend) Query(ctx context.Context, _ string) (workflows.State, error) {
	return workflows.State{}, b.wait(ctx)
}
func (b *lifetimeBackend) Submit(ctx context.Context, _ string, _ workflows.Input, _ workflows.MessageInput) (workflows.MessageReceipt, error) {
	return workflows.MessageReceipt{}, b.wait(ctx)
}
func (b *lifetimeBackend) Stop(ctx context.Context, _ string) error { return b.wait(ctx) }

func awaitLifetime[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
		var zero T
		return zero
	}
}

func TestSessionLifetimeCancellation(t *testing.T) {
	for _, operation := range []string{"query", "submit", "stop"} {
		for _, shutdown := range []string{"close", "parent"} {
			t.Run(operation+"/"+shutdown, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				b := &lifetimeBackend{started: make(chan context.Context, 1)}
				m := New(b, Options{Context: parent, SessionID: "old"})
				defer m.Close()
				start := func() (context.Context, <-chan struct{}) {
					var cmd tea.Cmd
					switch operation {
					case "query":
						cmd = m.queryCmd()
					case "submit":
						cmd = m.submitCmd("hello", "followUp", false)
					case "stop":
						cmd = m.actionCmd("stop")
					}
					done := make(chan struct{})
					go func() { cmd(); close(done) }()
					return awaitLifetime(t, b.started), done
				}
				old, oldDone := start()
				m.switchSession("new")
				awaitLifetime(t, old.Done())
				awaitLifetime(t, oldDone)
				current, done := start()
				if current.Err() != nil {
					t.Fatal("session switch canceled new session")
				}
				if shutdown == "close" {
					m.Close()
					m.Close()
				} else {
					cancel()
				}
				awaitLifetime(t, current.Done())
				awaitLifetime(t, done)
				m.switchSession("after shutdown")
				future, futureDone := start()
				awaitLifetime(t, future.Done())
				awaitLifetime(t, futureDone)
			})
		}
	}
}

func TestCloseWithoutParent(t *testing.T) {
	m := New(nil, Options{})
	m.Close()
	m.Close()
	if m.ctx.Err() != context.Canceled {
		t.Fatal("default lifetime not canceled")
	}
}
