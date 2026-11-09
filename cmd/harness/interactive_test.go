package main

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tui"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type shutdownBackend struct {
	tui.Backend
	started chan context.Context
	done    chan struct{}
}

func (b *shutdownBackend) wait(ctx context.Context) error {
	b.started <- ctx
	<-ctx.Done()
	close(b.done)
	return ctx.Err()
}
func (b *shutdownBackend) Query(ctx context.Context, _ string) (workflows.State, error) {
	return workflows.State{}, b.wait(ctx)
}
func (b *shutdownBackend) Stop(ctx context.Context, _ string) error { return b.wait(ctx) }

func awaitShutdown[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown")
		var zero T
		return zero
	}
}

func TestInteractiveDisposesBeforeClientCleanup(t *testing.T) {
	for _, exit := range []string{"external context", "Run error", "normal"} {
		t.Run(exit, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := &shutdownBackend{started: make(chan context.Context, 1), done: make(chan struct{})}
			sentinel := errors.New("program initialization failed")
			var call context.Context
			var want error
			if exit == "Run error" {
				want = sentinel
			}
			func() {
				// Mirrors interactive's deferred client.Close, without any real client.
				defer func() {
					if call == nil || call.Err() != context.Canceled {
						t.Error("client cleanup preceded backend context cancellation")
					}
				}()
				err := runInteractiveUI(ctx, b, tui.Options{SessionID: "test", DataDir: t.TempDir()}, func(m *tui.Model) error {
					if exit == "external context" {
						observed := make(chan context.Context, 1)
						go func() {
							select {
							case c := <-b.started:
								observed <- c
								cancel()
							case <-ctx.Done():
							}
						}()
						// A real Bubble Tea program, canceled externally, not by a key event.
						timer := time.AfterFunc(3*time.Second, cancel)
						defer timer.Stop()
						_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler()).Run()
						call = awaitShutdown(t, observed)
						if !errors.Is(err, tea.ErrProgramKilled) {
							t.Errorf("Run error = %v", err)
						}
						return err
					}
					// Model a Run return while a backend command is still outstanding.
					_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					go cmd()
					call = awaitShutdown(t, b.started)
					return want
				})
				if !errors.Is(err, want) {
					t.Errorf("got %v, want %v", err, want)
				}
			}()
			awaitShutdown(t, b.done)
		})
	}
}
