package main

import (
	"context"
	"errors"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
)

// A repository failure is a supervision failure, not proof of recovered SQL
// from empty polls. Cancel and JOIN HTTP and dispatcher before closing clients.
func supervise(parent context.Context, httpRun, dispatchRun func(context.Context) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- httpRun(ctx) }()
	go func() { done <- dispatchRun(ctx) }()
	first := <-done
	cancel()
	second := <-done
	for _, err := range []error{first, second} {
		if errors.Is(err, dispatch.ErrRepositoryUnavailable) {
			return dispatch.ErrRepositoryUnavailable
		}
	}
	for _, err := range []error{first, second} {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}
