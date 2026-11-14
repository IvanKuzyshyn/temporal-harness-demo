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

type reconcileStep struct {
	claim     bool
	claimErr  error
	settleErr error
}

type reconcileRepository struct {
	// Without a script, every claim or acknowledgement fails persistently.
	failAck     bool
	script      []reconcileStep
	calls       atomic.Int32
	settlements atomic.Int32
	observed    chan int
	settled     chan struct{}
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
		if n > len(r.script) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		step := r.script[n-1]
		if step.claimErr != nil || !step.claim {
			return nil, step.claimErr
		}
	} else if !r.failAck {
		return nil, privateRepositoryError
	}
	return []storage.CommandClaim{{
		Command: chat.StoredCommand{
			Input:   chat.CommandInput{RequestID: "11111111-1111-4111-8111-111111111111", ConversationID: "22222222-2222-4222-8222-222222222222", Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: "hello"}},
			Receipt: chat.CommandReceipt{CommandID: "33333333-3333-4333-8333-333333333333", TurnID: "44444444-4444-4444-8444-444444444444", PayloadDigest: strings.Repeat("a", 64), DispatchState: "pending", ResultState: "pending"}},
		WorkflowID: "web-chat/22222222-2222-4222-8222-222222222222", Model: "qwen3.5:9b", Token: "55555555-5555-4555-8555-555555555555", Sequence: 1, Attempts: 1,
	}}, nil
}
func (r *reconcileRepository) SettleClaim(ctx context.Context, _ storage.CommandClaim, _ string, _ []byte, _ time.Duration) error {
	r.settlements.Add(1)
	if r.script == nil {
		return privateRepositoryError
	}
	err := r.script[int(r.calls.Load())-1].settleErr
	if err == nil && r.settled != nil {
		select {
		case r.settled <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
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

// Failed acknowledgements leave leases behind. Empty polls during those leases
// must not erase the failure budget, even though claiming itself still works.
func TestRunReportsAcknowledgementFailuresAcrossEmptyPolls(t *testing.T) {
	failedAck := reconcileStep{claim: true, settleErr: privateRepositoryError}
	repo := &reconcileRepository{script: []reconcileStep{
		failedAck, {}, {}, {}, failedAck, {}, {}, {}, failedAck,
	}}
	d, err := dispatch.New(repo, admittingGateway{}, dispatch.Options{PollInterval: time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = d.Run(ctx)
	require.ErrorIs(t, err, dispatch.ErrRepositoryUnavailable, "empty lease-wait polls must not hide persistent acknowledgement errors until caller timeout")
	require.EqualError(t, err, "dispatcher repository unavailable")
	require.NotErrorIs(t, err, privateRepositoryError)
	require.EqualValues(t, 9, repo.calls.Load())
	require.EqualValues(t, 3, repo.settlements.Load())
}

func TestRunTransientFailureBudgetResetsAfterSuccessfulSettlement(t *testing.T) {
	failedAck := reconcileStep{claim: true, settleErr: privateRepositoryError}
	successfulAck := reconcileStep{claim: true}
	repo := &reconcileRepository{script: []reconcileStep{
		failedAck, {}, {}, failedAck, {}, successfulAck,
		failedAck, {}, {}, failedAck, successfulAck,
	}, settled: make(chan struct{}, 2)}
	d, err := dispatch.New(repo, admittingGateway{}, dispatch.Options{PollInterval: time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for successes := 0; successes < 2; {
		select {
		case <-repo.settled:
			successes++
		case err := <-done:
			t.Fatalf("Run stopped before recovering through successful settlements: %v", err)
		case <-timeout.C:
			t.Fatal("Run did not retry/settle")
		}
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel after recovery")
	}
	require.EqualValues(t, 6, repo.settlements.Load(), "two pairs of failed acknowledgements must each recover through a successful settlement")
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
