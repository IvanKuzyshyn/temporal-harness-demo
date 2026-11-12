package activities

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/stretchr/testify/require"
	"testing"
)

type preparingWebRepository struct {
	*cleanupRepository
	WebRepository
	started, release chan struct{}
}

func (r *preparingWebRepository) PrepareWebActions(context.Context, chat.TurnRef) (chat.ModelStepResult, error) {
	close(r.started)
	<-r.release
	return chat.ModelStepResult{}, nil
}
func TestWebModelCleanupIncludesActionPreparation(t *testing.T) {
	r := &preparingWebRepository{cleanupRepository: &cleanupRepository{ownershipRepository: ownershipRepository{cached: true}, acked: make(chan context.Context, 1)}, started: make(chan struct{}), release: make(chan struct{})}
	a := ChatActivities{Repository: r}
	done := make(chan error, 1)
	go func() { _, err := a.ChatStep(context.Background(), chat.TurnRef{}); done <- err }()
	<-r.started
	early := len(r.acked)
	close(r.release)
	require.NoError(t, <-done)
	require.Zero(t, early, "the model body still owns persistence work until tool references are prepared")
	require.Len(t, r.acked, 1)
}
