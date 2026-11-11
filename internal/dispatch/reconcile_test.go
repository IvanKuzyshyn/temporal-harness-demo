package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

var privateRepositoryError = errors.New("SQL failed: postgres://webchat:secret-password@127.0.0.1/webchat SELECT private_column")

type reconcileRepository struct {
	// Without a script, every claim or acknowledgement fails persistently.
	failAck  bool
	script   []error
	calls    atomic.Int32
	observed chan int
}

func (r *reconcileRepository) ClaimCommands(ctx context.Context, _ int, _ time.Duration) ([]storage.CommandClaim, error) {
	n := int(r.calls.Add(1))
	if r.observed != nil {
		select {
		case r.observed <- n:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.script != nil {
		if n <= len(r.script) {
			return nil, r.script[n-1]
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if !r.failAck {
		return nil, privateRepositoryError
	}
	return []storage.CommandClaim{{
		Command: chat.StoredCommand{
			Input:   chat.CommandInput{RequestID: "11111111-1111-4111-8111-111111111111", ConversationID: "22222222-2222-4222-8222-222222222222", Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: "hello"}},
			Receipt: chat.CommandReceipt{CommandID: "33333333-3333-4333-8333-333333333333", TurnID: "44444444-4444-4444-8444-444444444444", PayloadDigest: strings.Repeat("a", 64), DispatchState: "pending", ResultState: "pending"}},
		WorkflowID: "web-chat/22222222-2222-4222-8222-222222222222", Model: "qwen3.5:9b", Token: "55555555-5555-4555-8555-555555555555", Sequence: 1, Attempts: 1,
	}}, nil
}
func (*reconcileRepository) SettleClaim(context.Context, storage.CommandClaim, string, []byte, time.Duration) error {
	return privateRepositoryError
}

type admittingGateway struct{}

func (admittingGateway) Submit(context.Context, dispatch.Command) (dispatch.CommandResult, error) {
	return dispatch.CommandResult{State: "accepted"}, nil
}

// The production Run loop, not just RunOnce, must notify its supervisor when
// repository errors persist. A gateway-connected flag cannot expose this fault.
func TestRunReportsPersistentRepositoryFailure(t *testing.T) {
	for _, ack := range []bool{false, true} {
		name := "claim"
		if ack {
			name = "acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			repo := &reconcileRepository{failAck: ack}
			d, err := dispatch.New(repo, admittingGateway{}, dispatch.Options{PollInterval: time.Millisecond})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = d.Run(ctx)
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded, "Run must expose persistent failure before its caller times out")
			require.ErrorIs(t, err, dispatch.ErrRepositoryUnavailable)
			require.EqualError(t, err, "dispatcher repository unavailable")
			require.NotErrorIs(t, err, privateRepositoryError, "private cause must not be exposed to a logger/supervisor")
			require.EqualValues(t, 3, repo.calls.Load(), "transient retry budget must be bounded")
		})
	}
}
func TestRunTransientFailureBudgetResetsAfterSuccess(t *testing.T) {
	repo := &reconcileRepository{script: []error{privateRepositoryError, privateRepositoryError, nil, privateRepositoryError, privateRepositoryError, nil}, observed: make(chan int, 10)}
	d, err := dispatch.New(repo, admittingGateway{}, dispatch.Options{PollInterval: time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		select {
		case n := <-repo.observed:
			if n == 6 {
				cancel()
				select {
				case err := <-done:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("Run did not cancel after recovery")
				}
				return
			}
		case err := <-done:
			t.Fatalf("Run stopped before recovering from transient failures: %v", err)
		case <-timeout.C:
			t.Fatal("Run did not retry/recover")
		}
	}
}
func TestRunCancellationInterruptsRepositoryRetryWait(t *testing.T) {
	repo := &reconcileRepository{observed: make(chan int, 1)}
	d, err := dispatch.New(repo, admittingGateway{}, dispatch.Options{PollInterval: time.Hour})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-repo.observed:
	case <-time.After(time.Second):
		t.Fatal("Run never attempted repository claim")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt retry wait")
	}
	require.EqualValues(t, 1, repo.calls.Load())
}
