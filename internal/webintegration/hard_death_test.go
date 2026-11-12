//go:build integration

package webintegration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/interceptor"
	tw "go.temporal.io/sdk/worker"
)

type hardDeathModel struct{ ready string }

func (m hardDeathModel) Chat(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
	// Production ChatModel has durably registered BEFORE entering this provider.
	if err := os.WriteFile(m.ready, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return llm.Response{}, err
	}
	select {} // Only the parent's SIGKILL ends this body; no graceful cleanup ack.
}

// Dedicated test executable entrypoint: never connects outside the runner's
// validated owned Temporal/Postgres endpoints. It polls only the model queue.
func TestHardDeathWorkerProcess(t *testing.T) {
	if os.Getenv("WEBCHAT_HARD_DEATH_PROCESS") != "1" {
		return
	}
	f := setup(t)
	ready := os.Getenv("WEBCHAT_HARD_DEATH_READY")
	require.NotEmpty(t, ready)
	a := &activities.ChatActivities{Repository: f.repo, Model: hardDeathModel{ready: ready}}
	worker := tw.New(f.sdk, w.WebLLMQueue, tw.Options{MaxConcurrentActivityExecutionSize: 1, WorkerStopTimeout: time.Second})
	worker.RegisterActivity(a.ChatModel)
	require.NoError(t, worker.Start())
	defer worker.Stop()
	<-time.After(90 * time.Second)
	t.Fatal("owned worker was not killed by parent")
}

func TestOrdinaryHardWorkerDeathRecoversCanonicalResponseAndFollowUp(t *testing.T) {
	f := setup(t)
	a := &activities.ChatActivities{Repository: f.repo}
	agents := tw.New(f.sdk, w.WebAgentsQueue, tw.Options{Interceptors: []interceptor.WorkerInterceptor{&attemptTimeouts{backoff: 100 * time.Millisecond}}})
	agents.RegisterWorkflow(w.WebChatSessionWorkflow)
	agents.RegisterActivity(a)
	require.NoError(t, agents.Start())
	t.Cleanup(agents.Stop)
	c := f.conversation(t, "qwen3.5:9b")
	dir := t.TempDir()
	ready := filepath.Join(dir, "provider-entered")
	log, err := os.Create(filepath.Join(dir, "worker.log"))
	require.NoError(t, err)
	t.Cleanup(func() { b, _ := os.ReadFile(log.Name()); t.Logf("owned child worker log:\n%s", b); _ = log.Close() })
	executable, err := os.Executable()
	require.NoError(t, err)
	child := exec.Command(executable, "-test.run=^TestHardDeathWorkerProcess$", "-test.timeout=100s")
	child.Env = append(os.Environ(), "WEBCHAT_HARD_DEATH_PROCESS=1", "WEBCHAT_HARD_DEATH_READY="+ready)
	child.Stdout, child.Stderr = log, log
	require.NoError(t, child.Start())
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	joined := false
	t.Cleanup(func() {
		if !joined {
			_ = child.Process.Kill() // Only the *os.Process returned by our own Start.
			select {
			case <-exited:
				joined = true
			case <-time.After(5 * time.Second):
				t.Error("owned child did not exit")
			}
		}
	})
	target := f.command(t, c, "ordinary generation survives hard worker death")
	_, err = f.gateway.Submit(f.ctx, target)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		b, e := os.ReadFile(ready)
		return e == nil && string(b) == strconv.Itoa(child.Process.Pid)
	}, 15*time.Second, 25*time.Millisecond)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: target.TurnID}
	var oldInvocation string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT id::text FROM chat_model_invocations WHERE turn_id=$1 AND attempt=1 AND cleanup_confirmed_at IS NULL", target.TurnID).Scan(&oldInvocation))
	follow := f.command(t, c, "normal queued follow-up after hard death")
	_, err = f.gateway.Submit(f.ctx, follow)
	require.NoError(t, err)
	require.NoError(t, child.Process.Kill())
	select {
	case err = <-exited:
		joined = true
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL did not join owned process")
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	status, ok := exit.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.True(t, status.Signaled())
	require.Equal(t, syscall.SIGKILL, status.Signal())
	t.Logf("SIGKILL joined test-owned worker PID%d after committed invocation %s", child.Process.Pid, oldInvocation)

	// A fresh worker process was killed above, not gracefully closed. This new
	// SDK worker receives Temporal's retry and uses the actual model/SQL protocol.
	replacementWorker := tw.New(f.sdk, w.WebLLMQueue, tw.Options{MaxConcurrentActivityExecutionSize: 1, WorkerStopTimeout: 5 * time.Second})
	replacementWorker.RegisterActivity((&activities.ChatActivities{Repository: f.repo, Model: mock.New()}).ChatModel)
	require.NoError(t, replacementWorker.Start())
	t.Cleanup(replacementWorker.Stop)
	var candidate chat.ChatModelResult
	require.Eventually(t, func() bool { var e error; candidate, e = f.repo.GetChatModelResult(f.ctx, ref); return e == nil }, 20*time.Second, 50*time.Millisecond)
	require.Equal(t, chat.LogicalID(target.TurnID, "model/0/attempt/2"), candidate.AttemptID)
	require.Eventually(t, func() bool {
		turn, e := f.repo.GetTurn(f.ctx, ref)
		if e == nil && turn.Status == "completed" {
			return true
		}
		value, e := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
		if e != nil {
			return false
		}
		var state w.WebSessionState
		return value.Get(&state) == nil && state.Status == "cleanup_interrupted"
	}, 10*time.Second, 25*time.Millisecond)
	turn, err := f.repo.GetTurn(f.ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "completed", turn.Status, "ordinary retry-safe completion must not require a dead process's cleanup ack")
	f.completed(t, follow)
	var pending, answers, events, controls int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM chat_model_invocations WHERE id=$1 AND cleanup_confirmed_at IS NULL", oldInvocation).Scan(&pending))
	require.Equal(t, 1, pending, "do not fabricate cleanup evidence from process death")
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM messages WHERE turn_id=$1 AND role='assistant' AND result_kind='final'", target.TurnID).Scan(&answers))
	require.Equal(t, 1, answers)
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM events WHERE turn_id=$1 AND type='turn.completed'", target.TurnID).Scan(&events))
	require.Equal(t, 1, events)
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM commands WHERE conversation_id=$1 AND kind IN ('stop','replace')", c.ID).Scan(&controls))
	require.Zero(t, controls)
	oldAttempt := chat.LogicalID(target.TurnID, "model/0/attempt/1")
	require.ErrorIs(t, f.repo.SaveChatModelResult(f.ctx, chat.ChatModelResult{TurnRef: ref, Text: "late stale overwrite", AttemptID: oldAttempt}), storage.ErrConflict)
	require.ErrorIs(t, f.repo.RegisterChatModelInvocation(f.ctx, ref, oldInvocation, 1), storage.ErrStaleChatAttempt)
	require.ErrorIs(t, f.repo.PersistTurnResult(f.ctx, chat.TurnResult{TurnRef: ref, Status: "failed", Reason: "late attempt"}), storage.ErrConflict)
	after, err := f.repo.GetChatModelResult(f.ctx, ref)
	require.NoError(t, err)
	require.Equal(t, candidate, after)
	var ownerStatus, entry string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT status,coalesce(result_entry_id::text,'') FROM model_steps WHERE id=$1", candidate.AttemptID).Scan(&ownerStatus, &entry))
	require.Equal(t, "completed", ownerStatus)
	require.Equal(t, chat.LogicalID(target.TurnID, "final"), entry)
}
