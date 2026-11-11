//go:build integration

package webintegration_test

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	tc "go.temporal.io/sdk/client"
	"sync/atomic"
	"testing"
	"time"
)

type admissionCrash struct {
	*storage.Repository
	once   atomic.Bool
	marked chan struct{}
}

func (r *admissionCrash) RecordChatAdmission(ctx context.Context, a chat.Admission) error {
	if err := r.Repository.RecordChatAdmission(ctx, a); err != nil {
		return err
	}
	if !r.once.Swap(true) {
		close(r.marked)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func TestAdmissionMarkerCrashAndRolloverRetainsPendingTurn(t *testing.T) {
	f := setup(t)
	repo := &admissionCrash{Repository: f.repo, marked: make(chan struct{})}
	w := f.start(t, repo, mock.New())
	c := f.conversation(t, "qwen3.5:9b")
	_, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 1})
	require.NoError(t, err)
	cmd := f.command(t, c, "must survive marker before Update completion")
	submitted := make(chan error, 1)
	go func() { _, err := f.gateway.Submit(f.ctx, cmd); submitted <- err }()
	select {
	case <-repo.marked:
	case <-time.After(15 * time.Second):
		t.Fatal("no admission marker")
	}
	// CAN must wait for the handler; marker exists, but Update has not completed.
	select {
	case err := <-submitted:
		t.Fatalf("Update completed before marker activity: %v", err)
	default:
	}
	// A run at its budget must reject transiently before admission, not cache
	// a completed pending/error result. The identical Update ID retries below.
	next := f.command(t, c, "retry same Update ID after rollover backpressure")
	_, err = f.gateway.Submit(f.ctx, next)
	require.Error(t, err)
	require.NoError(t, w.Close())
	f.start(t, f.repo, mock.New())
	select {
	case err := <-submitted:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("admission not recovered")
	}
	f.completed(t, cmd)
	_, err = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	_, err = f.gateway.Submit(f.ctx, next)
	require.NoError(t, err)
	f.completed(t, next)
	page, err := f.repo.ListMessages(f.ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 4)
}
