//go:build integration

package webintegration_test

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type interruptedModel struct{ entered chan struct{} }

func (m *interruptedModel) Chat(ctx context.Context, _ llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	close(m.entered)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
func TestWorkerShutdownDuringInferenceResumes(t *testing.T) {
	f := setup(t)
	m := &interruptedModel{entered: make(chan struct{})}
	w := f.start(t, f.repo, m)
	c := f.conversation(t, "qwen3.5:9b")
	cmd := f.command(t, c, "resume after worker loss")
	_, err := f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	select {
	case <-m.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model not started")
	}
	stopping := time.Now()
	require.NoError(t, w.Close())
	require.Less(t, time.Since(stopping), 5*time.Second, "provider must join during graceful shutdown, not outlive the SDK stop deadline")
	f.start(t, f.repo, mock.New())
	f.completed(t, cmd)
	var attempts, answers int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM model_steps WHERE turn_id=$1", cmd.TurnID).Scan(&attempts))
	require.GreaterOrEqual(t, attempts, 2)
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM messages WHERE turn_id=$1 AND role='assistant'", cmd.TurnID).Scan(&answers))
	require.Equal(t, 1, answers)
	var running int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM model_steps WHERE turn_id=$1 AND status='running'", cmd.TurnID).Scan(&running))
	require.Zero(t, running, "interrupted attempts must not remain running after recovery")
}
