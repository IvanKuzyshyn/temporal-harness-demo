package activities

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type cleanupRepository struct {
	ownershipRepository
	registerErr, ackErr error
	registered          string
	acked               chan context.Context
}

func (r *cleanupRepository) RegisterChatModelInvocation(_ context.Context, _ chat.TurnRef, id string, _ int) error {
	r.registered = id
	return r.registerErr
}
func (r *cleanupRepository) AcknowledgeChatModelCleanup(ctx context.Context, _ chat.TurnRef, id string) error {
	if id != r.registered || !chat.ValidID(id) {
		return errors.New("wrong acknowledgment owner")
	}
	// Snapshot before the activity's deferred cancel expires this bounded context.
	if ctx.Err() != nil {
		return errors.New("cleanup inherited cancellation")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("unbounded cleanup context")
	}
	r.acked <- ctx
	return r.ackErr
}

type heldCleanupModel struct{ started, cancelled, release chan struct{} }

func (m *heldCleanupModel) Chat(ctx context.Context, _ llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	close(m.started)
	<-ctx.Done()
	close(m.cancelled)
	<-m.release
	return llm.Response{}, ctx.Err()
}
func TestChatModelAcknowledgesOnlyAfterBodyCleanup(t *testing.T) {
	ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
	repo := &cleanupRepository{acked: make(chan context.Context, 1)}
	m := &heldCleanupModel{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	a := ChatActivities{Repository: repo, Model: m}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.ChatModel(ctx, ref) }()
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	cancel()
	<-m.cancelled
	select {
	case <-repo.acked:
		t.Fatal("provider cleanup was acknowledged early")
	default:
	}
	close(m.release)
	require.Error(t, <-done)
	select {
	case <-repo.acked:
	default:
		t.Fatal("missing post-cleanup acknowledgment")
	}
}

type abortingCleanupModel struct{ goexit bool }

func (m abortingCleanupModel) Chat(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
	if m.goexit {
		runtime.Goexit()
	}
	panic("provider aborted without confirming cleanup")
}
func TestChatModelAbnormalExitDoesNotAcknowledgeCleanup(t *testing.T) {
	for _, goexit := range []bool{false, true} {
		repo := &cleanupRepository{acked: make(chan context.Context, 1)}
		a := ChatActivities{Repository: repo, Model: abortingCleanupModel{goexit: goexit}}
		ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }()
			_ = a.ChatModel(context.Background(), ref)
		}()
		<-done
		require.Empty(t, repo.acked, "panic/Goexit is not normal provider cleanup completion")
	}
}

func TestChatModelRegistrationAndCleanupFailuresCannotAuthorizeWork(t *testing.T) {
	ref := chat.TurnRef{ConversationID: chat.ConversationID(uuid.NewString()), TurnID: chat.TurnID(uuid.NewString())}
	t.Run("sealed admission", func(t *testing.T) {
		repo := &cleanupRepository{registerErr: storage.ErrStaleChatAttempt, acked: make(chan context.Context, 1)}
		a := ChatActivities{Repository: repo}
		require.ErrorIs(t, a.ChatModel(context.Background(), ref), storage.ErrStaleChatAttempt)
		require.Zero(t, repo.reads, "no cache/provider work after failed registration")
		require.Empty(t, repo.acked)
	})
	t.Run("lost cleanup acknowledgment", func(t *testing.T) {
		missing := errors.New("cleanup database unavailable")
		repo := &cleanupRepository{ownershipRepository: ownershipRepository{cached: true}, ackErr: missing, acked: make(chan context.Context, 1)}
		a := ChatActivities{Repository: repo}
		require.ErrorIs(t, a.ChatModel(context.Background(), ref), missing)
		require.Len(t, repo.acked, 1)
	})
}
