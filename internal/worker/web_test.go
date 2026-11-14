package worker

import (
	"context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	tc "go.temporal.io/sdk/client"
	tw "go.temporal.io/sdk/worker"
	"testing"
	"time"
)

func TestWebWorkerIsolationAndCleanup(t *testing.T) {
	c := &fakeClient{}
	var queues []string
	var workers []*fakeWorker
	r, err := startWeb(context.Background(), "test-address", &activities.ChatActivities{}, func(context.Context, tc.Options) (tc.Client, error) { return c, nil }, func(_ tc.Client, q string, o tw.Options) tw.Worker {
		queues = append(queues, q)
		require.Equal(t, 10*time.Second, o.WorkerStopTimeout)
		if q == workflows.WebLLMQueue {
			require.Equal(t, 1, o.MaxConcurrentActivityExecutionSize)
		}
		w := &fakeWorker{}
		workers = append(workers, w)
		return w
	})
	require.NoError(t, err)
	require.Equal(t, []string{"web-chat-agents", "web-chat-llm"}, queues)
	require.Len(t, workers[0].workflows, 1)
	require.Len(t, workers[0].activities, 5)
	require.Len(t, workers[1].activities, 1)
	require.NoError(t, r.Close())
	require.Equal(t, 1, c.closed)
	for _, w := range workers {
		require.Equal(t, 1, w.stopped)
	}
}
