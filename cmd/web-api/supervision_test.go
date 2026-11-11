package main

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestDispatcherFailureStopsSupervision(t *testing.T) {
	joined := make(chan struct{})
	err := supervise(context.Background(), func(ctx context.Context) error { <-ctx.Done(); close(joined); return nil }, func(context.Context) error { return dispatch.ErrRepositoryUnavailable })
	require.ErrorIs(t, err, dispatch.ErrRepositoryUnavailable)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("HTTP not joined")
	}
}
