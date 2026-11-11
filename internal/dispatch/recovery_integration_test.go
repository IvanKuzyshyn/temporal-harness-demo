//go:build integration

package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/stretchr/testify/require"
)

type lostAcknowledgement struct{ *storage.Repository }

func (r lostAcknowledgement) SettleClaim(context.Context, storage.CommandClaim, string, []byte, time.Duration) error {
	return errors.New("API dies after remote acceptance before DB ack")
}
func TestGatewayAcceptanceBeforeDatabaseAckRecovers(t *testing.T) {
	r, p := db(t)
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	a := enqueue(t, r, c.ID, chat.CommandMessage, "")
	_, e = p.Exec(ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE id<>$1", a.CommandID)
	require.NoError(t, e)
	g := &gateway{results: map[chat.CommandID]dispatch.CommandResult{}}
	d, e := dispatch.New(lostAcknowledgement{r}, g, dispatch.Options{})
	require.NoError(t, e)
	worked, e := d.RunOnce(ctx)
	require.True(t, worked)
	require.Error(t, e)
	before, e := r.GetCommand(ctx, c.ID, a.CommandID)
	require.NoError(t, e)
	require.Equal(t, "pending", before.ResultState)
	_, e = p.Exec(ctx, "UPDATE commands SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", a.CommandID)
	require.NoError(t, e)
	p.Close()
	r, p = db(t)
	d, e = dispatch.New(r, g, dispatch.Options{})
	require.NoError(t, e)
	worked, e = d.RunOnce(ctx)
	require.NoError(t, e)
	require.True(t, worked)
	after, e := r.GetCommand(ctx, c.ID, a.CommandID)
	require.NoError(t, e)
	require.Equal(t, "accepted", after.ResultState)
	require.Len(t, g.calls, 2)
	require.Equal(t, g.calls[0], g.calls[1])
	var n int
	require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM messages WHERE conversation_id=$1", c.ID).Scan(&n))
	require.Equal(t, 1, n)
}
func TestConcurrentClaimOwnership(t *testing.T) {
	r, p := db(t)
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	a := enqueue(t, r, c.ID, chat.CommandMessage, "")
	_, e = p.Exec(ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE id<>$1", a.CommandID)
	require.NoError(t, e)
	var wg sync.WaitGroup
	claims := make(chan []storage.CommandClaim, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); out, e := r.ClaimCommands(ctx, 1, time.Second); claims <- out; errs <- e }()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	n := 0
	for out := range claims {
		n += len(out)
	}
	require.Equal(t, 1, n)
}
