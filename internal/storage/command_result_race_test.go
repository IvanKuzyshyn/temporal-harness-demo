//go:build integration

package storage_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type receiptReadKey struct{}

// QueryRow.Scan closes its rows before returning. This test-only tracer pauses
// RecordCommandResult after its receipt SELECT, without hooks in production code.
type receiptReadBarrier struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *receiptReadBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	target := strings.HasPrefix(data.SQL, "SELECT id::text,") && strings.Contains(data.SQL, "FROM commands WHERE conversation_id=$1 AND id=$2")
	return context.WithValue(ctx, receiptReadKey{}, target)
}
func (b *receiptReadBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if target, _ := ctx.Value(receiptReadKey{}).(bool); target {
		b.once.Do(func() {
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
}

// The old writer reads pending while holding only the conversation lock. The
// barrier lets the real SettleClaim win before that writer's unconditional
// UPDATE. With command-row locking, settlement instead waits for the old writer.
// pg_stat_activity proves the wait; no sleep is used to guess the interleaving.
func TestCommandResultMixedWriterRace(t *testing.T) {
	for _, tc := range []struct{ name, dispatch, state, body string }{
		{"no_pending_resurrection", "pending", "pending", `{}`},
		{"no_changed_terminal_state", "dispatched", "rejected", `{"state":"rejected"}`},
		{"no_changed_terminal_body", "dispatched", "accepted", `{"state":"accepted","count":2}`},
		{"identical_terminal_is_idempotent", "dispatched", "accepted", `{"state":"accepted","count":1e3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := database(t)
			c := conversation(t, r)
			a, err := r.AcceptCommand(ctx, input(c.ID))
			require.NoError(t, err)
			runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			claim := storage.CommandClaim{Command: chat.StoredCommand{Receipt: a}, Token: uuid.NewString()}
			_, err = p.Exec(runCtx, "UPDATE commands SET lease_token=$2,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1", a.CommandID, claim.Token)
			require.NoError(t, err)
			barrier := &receiptReadBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			defer release()
			cfg, err := pgxpool.ParseConfig(os.Getenv("WEBCHAT_TEST_DATABASE_URL"))
			require.NoError(t, err)
			cfg.ConnConfig.Tracer = barrier
			legacyPool, err := pgxpool.NewWithConfig(runCtx, cfg)
			require.NoError(t, err)
			t.Cleanup(legacyPool.Close)
			legacyDone := make(chan error, 1)
			go func() {
				legacyDone <- storage.New(legacyPool).RecordCommandResult(runCtx, c.ID, a.CommandID, tc.dispatch, tc.state, []byte(tc.body))
			}()
			select {
			case <-barrier.entered:
			case <-runCtx.Done():
				t.Fatal("legacy receipt read did not reach barrier")
			}
			cfg, err = pgxpool.ParseConfig(os.Getenv("WEBCHAT_TEST_DATABASE_URL"))
			require.NoError(t, err)
			application := "mixed_settle_" + uuid.NewString()
			cfg.ConnConfig.RuntimeParams["application_name"] = application
			settlementPool, err := pgxpool.NewWithConfig(runCtx, cfg)
			require.NoError(t, err)
			t.Cleanup(settlementPool.Close)
			settlementDone := make(chan error, 1)
			gatewayBody := []byte(`{"state":"accepted","count":1000}`)
			go func() {
				settlementDone <- storage.New(settlementPool).SettleClaim(runCtx, claim, "accepted", gatewayBody, time.Second)
			}()
			// Either settlement really commits (buggy legacy read), or PostgreSQL proves
			// it is blocked on the row locked by the fixed legacy receipt SELECT.
			settledFirst := false
			var settlementErr error
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
		wait:
			for {
				select {
				case settlementErr = <-settlementDone:
					settledFirst = true
					break wait
				case <-ticker.C:
					var blocked bool
					err = p.QueryRow(runCtx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')", application).Scan(&blocked)
					require.NoError(t, err)
					if blocked {
						break wait
					}
				case <-runCtx.Done():
					t.Fatal("settlement neither committed nor reached a database lock wait")
				}
			}
			release()
			var legacyErr error
			select {
			case legacyErr = <-legacyDone:
			case <-runCtx.Done():
				t.Fatal("legacy writer did not finish")
			}
			if !settledFirst {
				select {
				case settlementErr = <-settlementDone:
				case <-runCtx.Done():
					t.Fatal("settlement did not finish")
				}
			}
			final, err := r.GetCommand(runCtx, c.ID, a.CommandID)
			require.NoError(t, err)
			if settledFirst {
				require.NoError(t, settlementErr)
				require.Equal(t, "accepted", final.ResultState, "legacy writer must never overwrite/resurrect an already-settled receipt")
				require.JSONEq(t, string(gatewayBody), string(final.Result))
				if tc.name == "identical_terminal_is_idempotent" {
					require.NoError(t, legacyErr)
				} else {
					require.ErrorIs(t, legacyErr, storage.ErrConflict)
				}
			} else {
				require.NoError(t, legacyErr)
				if tc.state == "pending" {
					require.NoError(t, settlementErr)
					require.Equal(t, "accepted", final.ResultState)
					require.JSONEq(t, string(gatewayBody), string(final.Result))
				} else {
					require.ErrorIs(t, settlementErr, storage.ErrLeaseLost)
					require.Equal(t, tc.state, final.ResultState)
					require.JSONEq(t, tc.body, string(final.Result))
				}
			}
			require.Equal(t, "dispatched", final.DispatchState)
			// Once terminal, both APIs preserve their existing equality/conflict rules.
			require.NoError(t, r.RecordCommandResult(runCtx, c.ID, a.CommandID, final.DispatchState, final.ResultState, final.Result))
			require.ErrorIs(t, r.RecordCommandResult(runCtx, c.ID, a.CommandID, "pending", "pending", nil), storage.ErrConflict)
			require.ErrorIs(t, r.SettleClaim(runCtx, claim, "pending", nil, time.Second), storage.ErrLeaseLost)
		})
	}
}
