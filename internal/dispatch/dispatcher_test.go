//go:build integration

package dispatch_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func db(t *testing.T) (*storage.Repository, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	require.NotEmpty(t, raw)
	cfg, e := pgxpool.ParseConfig(raw)
	require.NoError(t, e)
	require.Equal(t, "webchat", cfg.ConnConfig.User)
	require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
	require.True(t, strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_"))
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, e)
	t.Cleanup(p.Close)
	return storage.New(p), p
}
func enqueue(t *testing.T, r *storage.Repository, c chat.ConversationID, kind chat.CommandKind, target chat.TurnID) chat.CommandReceipt {
	t.Helper()
	in := chat.CommandInput{ConversationID: c, RequestID: uuid.NewString(), Kind: kind, Payload: chat.CommandPayload{ExpectedActiveTurnID: target}}
	if kind == chat.CommandMessage || kind == chat.CommandReplace {
		in.Payload.Text = "hello"
	}
	a, e := r.AcceptCommand(ctx, in)
	require.NoError(t, e)
	return a
}

type gateway struct {
	calls                 []dispatch.Command
	results               map[chat.CommandID]dispatch.CommandResult
	failBefore, failAfter bool
}

func (g *gateway) Submit(_ context.Context, c dispatch.Command) (dispatch.CommandResult, error) {
	g.calls = append(g.calls, c)
	if g.failBefore {
		g.failBefore = false
		return dispatch.CommandResult{}, errors.New("unreachable")
	}
	result, ok := g.results[c.CommandID]
	if !ok {
		result = dispatch.CommandResult{State: "accepted"}
		g.results[c.CommandID] = result
	}
	if g.failAfter {
		g.failAfter = false
		return dispatch.CommandResult{}, errors.New("ambiguous acknowledgement")
	}
	return result, nil
}
func TestDispatchRetryUsesSameIdentity(t *testing.T) {
	r, p := db(t)
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	a := enqueue(t, r, c.ID, chat.CommandMessage, "")
	// Isolate scheduling from other tests' intentional pending commands without deleting them.
	_, e = p.Exec(ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE id<>$1", a.CommandID)
	require.NoError(t, e)
	g := &gateway{results: map[chat.CommandID]dispatch.CommandResult{}, failBefore: true, failAfter: true}
	d, e := dispatch.New(r, g, dispatch.Options{RetryBase: time.Millisecond, RetryMax: time.Millisecond})
	require.NoError(t, e)
	for i := 0; i < 3; i++ {
		worked, e := d.RunOnce(ctx)
		require.NoError(t, e)
		require.True(t, worked)
		time.Sleep(5 * time.Millisecond)
	}
	require.Len(t, g.calls, 3)
	require.Equal(t, g.calls[0], g.calls[1])
	require.Equal(t, g.calls[0], g.calls[2])
	require.Equal(t, a.CommandID, g.calls[0].CommandID)
	require.NotEmpty(t, g.calls[0].RequestID)
	require.NotEqual(t, string(a.CommandID), g.calls[0].RequestID)
	require.Equal(t, c.WorkflowID, g.calls[0].WorkflowID)
	require.Equal(t, a.PayloadDigest, g.calls[0].PayloadDigest)
	got, e := r.GetCommand(ctx, c.ID, a.CommandID)
	require.NoError(t, e)
	require.Equal(t, "accepted", got.ResultState)
	var n int
	require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM messages WHERE conversation_id=$1", c.ID).Scan(&n))
	require.Equal(t, 1, n)
}
func TestLeaseRecoveryFencingAndControlFairness(t *testing.T) {
	r, p := db(t)
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	a := enqueue(t, r, c.ID, chat.CommandMessage, "")
	b := enqueue(t, r, c.ID, chat.CommandMessage, "")
	stop := enqueue(t, r, c.ID, chat.CommandStop, a.TurnID)
	_, e = p.Exec(ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE conversation_id<>$1", c.ID)
	require.NoError(t, e)
	claims, e := r.ClaimCommands(ctx, 1, time.Second)
	require.NoError(t, e)
	require.Len(t, claims, 1)
	require.Equal(t, a.CommandID, claims[0].Command.Receipt.CommandID)
	other, e := r.ClaimCommands(ctx, 10, time.Second)
	require.NoError(t, e)
	require.Len(t, other, 1)
	require.Equal(t, stop.CommandID, other[0].Command.Receipt.CommandID)
	require.NoError(t, r.SettleClaim(ctx, other[0], "accepted", []byte(`{"state":"accepted"}`), time.Second))
	// Process dies after gateway acceptance, before DB acknowledgement: expire claim and recover with another token.
	_, e = p.Exec(ctx, "UPDATE commands SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", a.CommandID)
	require.NoError(t, e)
	recovered, e := r.ClaimCommands(ctx, 1, time.Second)
	require.NoError(t, e)
	require.Len(t, recovered, 1)
	require.Equal(t, a.CommandID, recovered[0].Command.Receipt.CommandID)
	require.NotEqual(t, claims[0].Token, recovered[0].Token)
	require.ErrorIs(t, r.SettleClaim(ctx, claims[0], "accepted", []byte(`{}`), time.Second), storage.ErrLeaseLost)
	require.NoError(t, r.SettleClaim(ctx, recovered[0], "accepted", []byte(`{}`), time.Second))
	next, e := r.ClaimCommands(ctx, 1, time.Second)
	require.NoError(t, e)
	require.Len(t, next, 1)
	require.Equal(t, b.CommandID, next[0].Command.Receipt.CommandID)
}
func TestUnavailableHeadDoesNotMonopolizeBatch(t *testing.T) {
	r, p := db(t)
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	a := enqueue(t, r, c.ID, chat.CommandMessage, "")
	c2, e := r.CreateConversation(ctx, "gemma4:e4b")
	require.NoError(t, e)
	b := enqueue(t, r, c2.ID, chat.CommandMessage, "")
	_, e = p.Exec(ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE id<>$1 AND id<>$2", a.CommandID, b.CommandID)
	require.NoError(t, e)
	g := &gateway{results: map[chat.CommandID]dispatch.CommandResult{}, failBefore: true}
	d, e := dispatch.New(r, g, dispatch.Options{})
	require.NoError(t, e)
	_, e = d.RunOnce(ctx)
	require.NoError(t, e)
	_, e = d.RunOnce(ctx)
	require.NoError(t, e)
	require.Len(t, g.calls, 2)
	require.Equal(t, b.CommandID, g.calls[1].CommandID)
	got, e := r.GetCommand(ctx, c2.ID, b.CommandID)
	require.NoError(t, e)
	require.Equal(t, "accepted", got.ResultState)
}
