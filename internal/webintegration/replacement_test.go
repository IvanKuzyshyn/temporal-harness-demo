//go:build integration

package webintegration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	chatclient "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

func (f *fixture) control(t *testing.T, c chat.Conversation, target chat.TurnID, kind chat.CommandKind, text string) chat.SessionCommand {
	t.Helper()
	in := chat.CommandInput{ConversationID: c.ID, RequestID: uuid.NewString(), Kind: kind, Payload: chat.CommandPayload{ExpectedActiveTurnID: target, Text: text}}
	rc, err := f.repo.AcceptCommand(f.ctx, in)
	require.NoError(t, err)
	var seq int64
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT sequence FROM commands WHERE id=$1", rc.CommandID).Scan(&seq))
	return chat.SessionCommand{CommandID: rc.CommandID, RequestID: in.RequestID, ConversationID: c.ID, TurnID: rc.TurnID, WorkflowID: c.WorkflowID, Model: c.Model, Kind: kind, Payload: in.Payload, PayloadDigest: rc.PayloadDigest, Sequence: seq}
}

type cancelBarrierModel struct {
	started, cancelling, release, replacementStarted, replacementRelease chan struct{}
	mu                                                                   sync.Mutex
	requests                                                             []llm.Request
}

func newCancelBarrierModel(t *testing.T) *cancelBarrierModel {
	m := &cancelBarrierModel{started: make(chan struct{}), cancelling: make(chan struct{}), release: make(chan struct{}), replacementStarted: make(chan struct{}), replacementRelease: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-m.release:
		default:
			close(m.release)
		}
		select {
		case <-m.replacementRelease:
		default:
			close(m.replacementRelease)
		}
	})
	return m
}
func (m *cancelBarrierModel) Chat(ctx context.Context, r llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, r)
	n := len(m.requests)
	m.mu.Unlock()
	if n == 1 {
		close(m.started)
		<-ctx.Done()
		close(m.cancelling)
		<-m.release
		return llm.Response{}, ctx.Err()
	}
	if n == 2 {
		close(m.replacementStarted)
		select {
		case <-m.replacementRelease:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	return mock.New().Chat(ctx, r, emit)
}

// Hold only cancellation settlement. The production activity batch times out
// during worker shutdown; replay/retry uses the real repository after restart.
type cancellingSettlementBarrier struct {
	*storage.Repository
	entered chan struct{}
	once    sync.Once
}

func (r *cancellingSettlementBarrier) PersistTurnResult(ctx context.Context, a chat.TurnResult) error {
	if a.Status == "cancelled" {
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		return ctx.Err()
	}
	return r.Repository.PersistTurnResult(ctx, a)
}

func TestReplacementUnconfirmedCleanupNeverStartsNextTurn(t *testing.T) {
	f := setup(t)
	m := newCancelBarrierModel(t)
	f.start(t, f.repo, m)
	t.Cleanup(func() {
		select {
		case <-m.release:
		default:
			close(m.release)
		}
	})
	c := f.conversation(t, "qwen3.5:9b")
	first := f.command(t, c, "uncooperative cleanup")
	_, err := f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	waitBarrier(t, m.started)
	replacement := f.control(t, c, first.TurnID, chat.CommandReplace, "must not overlap")
	_, err = f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	waitBarrier(t, m.cancelling)
	require.Eventually(t, func() bool {
		v, e := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
		if e != nil {
			return false
		}
		var s workflows.WebSessionState
		if v.Get(&s) != nil {
			return false
		}
		return s.Status == "cancelling_interrupted"
	}, 35*time.Second, 100*time.Millisecond)
	select {
	case <-m.replacementStarted:
		t.Fatal("replacement ran after unconfirmed cleanup timeout")
	default:
	}
	close(m.release)
	turn, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: replacement.TurnID})
	require.NoError(t, err)
	require.Equal(t, "queued", turn.Status)
}

func TestReplacementSurvivesWorkerTemporalPostgresRestartDuringCancelling(t *testing.T) {
	f := setup(t)
	m := newCancelBarrierModel(t)
	repo := &cancellingSettlementBarrier{Repository: f.repo, entered: make(chan struct{})}
	runner := f.start(t, repo, m)
	t.Cleanup(func() {
		select {
		case <-m.release:
		default:
			close(m.release)
		}
	})
	c := f.conversation(t, "qwen3.5:9b")
	first := f.command(t, c, "original survives restart")
	_, err := f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	waitBarrier(t, m.started)
	follow := f.command(t, c, "follow-up survives restart")
	_, err = f.gateway.Submit(f.ctx, follow)
	require.NoError(t, err)
	replacement := f.control(t, c, first.TurnID, chat.CommandReplace, "replacement survives restart")
	accepted, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, "accepted", accepted.State)
	waitBarrier(t, m.cancelling)
	close(m.release)
	waitBarrier(t, repo.entered)
	var before time.Time
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT pg_postmaster_start_time()").Scan(&before))
	require.NoError(t, runner.Close())
	// The shell runner owns/restarts BOTH actual servers. No test can target the
	// developer stack; request/ack files exist only under its unique temp directory.
	request := os.Getenv("WEBCHAT_TEST_RESTART_REQUEST")
	require.NotEmpty(t, request, "replacement runner must provide real server restart")
	require.True(t, strings.HasPrefix(filepath.Base(filepath.Dir(request)), "webchat-storage."))
	require.NoError(t, os.WriteFile(request, []byte("restart"), 0600))
	ack := request + ".done"
	var address string
	require.Eventually(t, func() bool {
		b, e := os.ReadFile(ack)
		address = strings.TrimSpace(string(b))
		return e == nil && strings.HasPrefix(address, "127.0.0.1:")
	}, 60*time.Second, 100*time.Millisecond)
	raw, err := url.Parse(os.Getenv("WEBCHAT_TEST_DATABASE_URL"))
	require.NoError(t, err)
	raw.Host = address
	// Later tests in this serial process must use the restarted published port.
	require.NoError(t, os.Setenv("WEBCHAT_TEST_DATABASE_URL", raw.String()))
	pool, err := pgxpool.New(f.ctx, raw.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	f.pool = pool
	f.repo = storage.New(pool)
	f.gateway = chatclient.NewWebGateway(f.sdk, f.repo)
	var after time.Time
	require.NoError(t, pool.QueryRow(f.ctx, "SELECT pg_postmaster_start_time()").Scan(&after))
	require.True(t, after.After(before))
	f.start(t, f.repo, mock.New())
	f.completed(t, replacement)
	f.completed(t, follow)
	old, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: first.TurnID})
	require.NoError(t, err)
	require.Equal(t, "cancelled", old.Status)
	retry, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, accepted, retry)
	var answers, users, events int
	require.NoError(t, pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE role='assistant'),count(*) FILTER(WHERE role='user') FROM messages WHERE conversation_id=$1`, c.ID).Scan(&answers, &users))
	require.Equal(t, 2, answers)
	require.Equal(t, 3, users)
	require.NoError(t, pool.QueryRow(f.ctx, `SELECT count(*) FROM events WHERE conversation_id=$1 AND type='turn.cancelled'`, c.ID).Scan(&events))
	require.Equal(t, 1, events)
	t.Log(fmt.Sprintf("real worker + Temporal server + PostgreSQL restart during cancelling; postmaster %s -> %s", before, after))
}

func waitBarrier(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatal("barrier timed out")
	}
}

// Real server cancellation, not the SDK test environment's immediate canceled
// future: withholding provider cleanup must withhold all replacement inference.
func TestReplacementRealJoinPriorityRetryConflictAndStaleStop(t *testing.T) {
	f := setup(t)
	m := newCancelBarrierModel(t)
	f.start(t, f.repo, m)
	// Release provider barriers before worker cleanup on any assertion failure.
	t.Cleanup(func() {
		select {
		case <-m.release:
		default:
			close(m.release)
		}
		select {
		case <-m.replacementRelease:
		default:
			close(m.replacementRelease)
		}
	})
	c := f.conversation(t, "qwen3.5:9b")
	run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 4})
	require.NoError(t, err)
	first := f.command(t, c, "original target prompt")
	_, err = f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	waitBarrier(t, m.started)
	follow := f.command(t, c, "queued follow-up")
	_, err = f.gateway.Submit(f.ctx, follow)
	require.NoError(t, err)
	replacement := f.control(t, c, first.TurnID, chat.CommandReplace, "correcting prompt")
	got, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, "accepted", got.State)
	require.Equal(t, first.TurnID, got.TargetTurnID)
	require.Equal(t, replacement.TurnID, got.ReplacementTurnID)
	competing := f.control(t, c, first.TurnID, chat.CommandReplace, "losing correction")
	loser, err := f.gateway.Submit(f.ctx, competing)
	require.NoError(t, err)
	require.Equal(t, "rejected", loser.State)
	require.Equal(t, "control_conflict", loser.Code)
	saved, err := f.repo.CheckChatCommand(f.ctx, competing)
	require.NoError(t, err)
	require.Equal(t, loser, saved)
	waitBarrier(t, m.cancelling)
	select {
	case <-m.replacementStarted:
		t.Fatal("replacement overlapped target cleanup")
	case <-time.After(250 * time.Millisecond):
	}
	target, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: first.TurnID})
	require.NoError(t, err)
	require.Equal(t, "cancelling", target.Status)
	close(m.release)
	waitBarrier(t, m.replacementStarted)
	target, err = f.repo.GetTurn(f.ctx, target.TurnRef)
	require.NoError(t, err)
	require.Equal(t, "cancelled", target.Status)
	retry, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, got, retry)
	stale := f.control(t, c, first.TurnID, chat.CommandStop, "")
	staleResult, err := f.gateway.Submit(f.ctx, stale)
	require.NoError(t, err)
	require.Equal(t, "stale_target", staleResult.Code)
	close(m.replacementRelease)
	f.completed(t, replacement)
	f.completed(t, follow)
	m.mu.Lock()
	requests := append([]llm.Request(nil), m.requests...)
	m.mu.Unlock()
	require.Len(t, requests, 3)
	require.Equal(t, "correcting prompt", requests[1].Messages[len(requests[1].Messages)-1].Content)
	require.Equal(t, "queued follow-up", requests[2].Messages[len(requests[2].Messages)-1].Content)
	followContext, _ := json.Marshal(requests[2])
	require.Contains(t, string(followContext), "correcting prompt", "follow-up executes AFTER replacement and must see its completed exchange despite earlier prompt acceptance")
	raw, _ := json.Marshal(requests[1])
	require.Contains(t, string(raw), "original target prompt")
	page, err := f.repo.ListMessages(f.ctx, c.ID, "", 30)
	require.NoError(t, err)
	var answers int
	for _, m := range page.Messages {
		if m.Role == "assistant" {
			answers++
			require.NotEqual(t, first.TurnID, m.TurnID)
		}
	}
	require.Equal(t, 2, answers)
	d := onlyConversation(t, f, c)
	for i := 0; i < 5; i++ {
		dispatchOne(t, f, d)
	}
	ui, err := f.repo.GetCommand(f.ctx, c.ID, competing.CommandID)
	require.NoError(t, err)
	require.Equal(t, "rejected", ui.ResultState)
	var uiResult chat.AdmissionResult
	require.NoError(t, json.Unmarshal(ui.Result, &uiResult))
	require.Equal(t, loser, uiResult)
	// Assert the actual run chain, rather than inferring CAN from a running
	// latest execution. The low admission budget checkpoints the cancellation;
	// model scheduling across the complete chain must preserve priority.
	id, rollovers := run.GetRunID(), 0
	var scheduled []chat.TurnID
	for {
		next := ""
		it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, id, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for it.HasNext() {
			event, e := it.Next()
			require.NoError(t, e)
			if a := event.GetWorkflowExecutionContinuedAsNewEventAttributes(); a != nil {
				next = a.NewExecutionRunId
				rollovers++
			}
			if a := event.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == workflows.WebModelActivity {
				var ref chat.TurnRef
				require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(a.Input, &ref))
				scheduled = append(scheduled, ref.TurnID)
			}
		}
		if next == "" {
			break
		}
		id = next
	}
	require.GreaterOrEqual(t, rollovers, 1)
	require.Equal(t, []chat.TurnID{first.TurnID, replacement.TurnID, follow.TurnID}, scheduled)
}
