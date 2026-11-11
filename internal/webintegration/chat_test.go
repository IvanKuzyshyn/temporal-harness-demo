//go:build integration

// These tests use test-owned REAL Postgres and Temporal. Only inference is mocked
// unless WEBCHAT_TEST_LIVE_MODELS=1 explicitly requests the two exact providers.
package webintegration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	chatclient "github.com/IvanKuzyshyn/temporal-harness-demo/internal/client"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
)

type fixture struct {
	ctx     context.Context
	repo    *storage.Repository
	pool    *pgxpool.Pool
	sdk     tc.Client
	gateway *chatclient.WebGateway
	address string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	raw := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	cfg, err := pgxpool.ParseConfig(raw)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
	require.True(t, strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_"))
	require.Equal(t, "webchat", cfg.ConnConfig.User)
	addr := os.Getenv("WEBCHAT_TEST_TEMPORAL_ADDRESS")
	require.True(t, strings.HasPrefix(addr, "127.0.0.1:"))
	require.NotEqual(t, "127.0.0.1:7233", addr, "never use shared queue")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	t.Cleanup(cancel)
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	sdk, err := tc.DialContext(ctx, tc.Options{HostPort: addr})
	require.NoError(t, err)
	t.Cleanup(sdk.Close)
	return &fixture{ctx: ctx, repo: storage.New(p), pool: p, sdk: sdk, gateway: chatclient.NewWebGateway(sdk), address: addr}
}
func (f *fixture) start(t *testing.T, repo activities.ChatRepository, m llm.Model) *worker.Runner {
	t.Helper()
	r, err := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: repo, Model: m})
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })
	return r
}
func (f *fixture) conversation(t *testing.T, model string) chat.Conversation {
	t.Helper()
	c, err := f.repo.CreateConversation(f.ctx, model)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.sdk.TerminateWorkflow(ctx, c.WorkflowID, "", "test cleanup")
	})
	return c
}
func (f *fixture) command(t *testing.T, c chat.Conversation, text string) chat.SessionCommand {
	t.Helper()
	in := chat.CommandInput{ConversationID: c.ID, RequestID: uuid.NewString(), Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: text}}
	rc, err := f.repo.AcceptCommand(f.ctx, in)
	require.NoError(t, err)
	var seq int64
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT sequence FROM commands WHERE id=$1", rc.CommandID).Scan(&seq))
	return chat.SessionCommand{CommandID: rc.CommandID, ConversationID: c.ID, TurnID: rc.TurnID, RequestID: in.RequestID, Kind: in.Kind, Payload: in.Payload, PayloadDigest: rc.PayloadDigest, Sequence: seq, WorkflowID: c.WorkflowID, Model: c.Model}
}
func (f *fixture) completed(t *testing.T, c chat.SessionCommand) {
	t.Helper()
	require.Eventually(t, func() bool {
		r, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ConversationID, TurnID: c.TurnID})
		return e == nil && r.Status == "completed"
	}, 90*time.Second, 50*time.Millisecond)
}
func TestContinueAsNewCommandDedup(t *testing.T) {
	f := setup(t)
	f.start(t, f.repo, mock.New())
	c := f.conversation(t, "qwen3.5:9b")
	run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 10})
	require.NoError(t, err)
	first := f.command(t, c, "first")
	res, err := f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	require.Equal(t, "accepted", res.State)
	f.completed(t, first)
	for n := 2; n <= 70; n++ {
		cmd := f.command(t, c, fmt.Sprint(n))
		res, err = f.gateway.Submit(f.ctx, cmd)
		require.NoError(t, err)
		require.Equal(t, "accepted", res.State)
		f.completed(t, cmd)
	}
	res, err = f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	require.Equal(t, "accepted", res.State)
	time.Sleep(300 * time.Millisecond)
	var total, answers int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*),count(*) FILTER(WHERE role='assistant') FROM messages WHERE conversation_id=$1", c.ID).Scan(&total, &answers))
	require.Equal(t, 140, total)
	require.Equal(t, 70, answers)
	// Inspect the ACTUAL history chain, not an SDK test environment claim.
	id := run.GetRunID()
	rollovers, modelTasks := 0, 0
	for {
		next := ""
		it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, id, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for it.HasNext() {
			e, err := it.Next()
			require.NoError(t, err)
			if a := e.GetWorkflowExecutionContinuedAsNewEventAttributes(); a != nil {
				next = a.NewExecutionRunId
				rollovers++
			}
			if a := e.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == workflows.WebModelActivity {
				modelTasks++
			}
		}
		if next == "" {
			break
		}
		id = next
	}
	require.GreaterOrEqual(t, rollovers, 7)
	require.Equal(t, 70, modelTasks)
	t.Logf("real Temporal history: %d rollovers, %d model tasks, delayed command #1 after 69 later admissions", rollovers, modelTasks)
}

type lostAck struct {
	*storage.Repository
	once atomic.Bool
}

func (r *lostAck) SettleClaim(ctx context.Context, c storage.CommandClaim, s string, b []byte, d time.Duration) error {
	if !r.once.Swap(true) {
		return errors.New("test lost database acknowledgement")
	}
	return r.Repository.SettleClaim(ctx, c, s, b, d)
}
func TestLostGatewayAcknowledgement(t *testing.T) {
	f := setup(t)
	f.start(t, f.repo, mock.New())
	c := f.conversation(t, "gemma4:e4b")
	cmd := f.command(t, c, "one durable answer")
	// Other tests' terminal workflow commands are not in the dispatcher lane.
	_, err := f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE conversation_id<>$1", c.ID)
	require.NoError(t, err)
	d, err := dispatch.New(&lostAck{Repository: f.repo}, f.gateway, dispatch.Options{})
	require.NoError(t, err)
	worked, err := d.RunOnce(f.ctx)
	require.True(t, worked)
	require.Error(t, err)
	f.completed(t, cmd)
	_, err = f.pool.Exec(f.ctx, "UPDATE commands SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", cmd.CommandID)
	require.NoError(t, err)
	d, err = dispatch.New(f.repo, f.gateway, dispatch.Options{})
	require.NoError(t, err)
	worked, err = d.RunOnce(f.ctx)
	require.True(t, worked)
	require.NoError(t, err)
	rc, err := f.repo.GetCommand(f.ctx, c.ID, cmd.CommandID)
	require.NoError(t, err)
	require.Equal(t, "accepted", rc.ResultState)
	page, err := f.repo.ListMessages(f.ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
}

type countModel struct {
	calls atomic.Int32
	base  llm.Model
}

func (m *countModel) Chat(ctx context.Context, r llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	m.calls.Add(1)
	return m.base.Chat(ctx, r, emit)
}

type failAfterSave struct {
	*storage.Repository
	once atomic.Bool
}

func (r *failAfterSave) SaveChatModelResult(ctx context.Context, result chat.ChatModelResult) error {
	if err := r.Repository.SaveChatModelResult(ctx, result); err != nil {
		return err
	}
	if !r.once.Swap(true) {
		return errors.New("test lost activity acknowledgement after durable result")
	}
	return nil
}
func TestModelAttemptRetryOneLogicalAnswer(t *testing.T) {
	f := setup(t)
	m := &countModel{base: mock.New()}
	f.start(t, &failAfterSave{Repository: f.repo}, m)
	c := f.conversation(t, "qwen3.5:9b")
	cmd := f.command(t, c, "cache before retry")
	_, err := f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	f.completed(t, cmd)
	require.EqualValues(t, 1, m.calls.Load())
	page, err := f.repo.ListMessages(f.ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	// A real retry exists, not merely two direct method calls.
	it := f.sdk.GetWorkflowHistory(f.ctx, c.WorkflowID, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	retry := false
	for it.HasNext() {
		e, err := it.Next()
		require.NoError(t, err)
		if a := e.GetActivityTaskStartedEventAttributes(); a != nil && a.Attempt > 1 {
			retry = true
		}
	}
	require.True(t, retry, "real activity attempt >1 recorded")
}

type settlementGate struct {
	*storage.Repository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *settlementGate) PersistTurnResult(ctx context.Context, result chat.TurnResult) error {
	r.once.Do(func() {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	return r.Repository.PersistTurnResult(ctx, result)
}
func TestDatabaseOutageDuringFinalPersistence(t *testing.T) {
	f := setup(t)
	container := os.Getenv("WEBCHAT_TEST_POSTGRES_CONTAINER")
	require.True(t, strings.HasPrefix(container, "webchat-storage-"))
	repo := &settlementGate{Repository: f.repo, entered: make(chan struct{}), release: make(chan struct{})}
	f.start(t, repo, mock.New())
	c := f.conversation(t, "qwen3.5:9b")
	cmd := f.command(t, c, "never falsely complete")
	_, err := f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	select {
	case <-repo.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("settlement not reached")
	}
	events, err := f.repo.ListEvents(f.ctx, c.ID, 0, 20)
	require.NoError(t, err)
	require.Empty(t, events)
	out, err := exec.Command("docker", "pause", container).CombinedOutput()
	require.NoError(t, err, string(out))
	paused := true
	defer func() {
		if paused {
			exec.Command("docker", "unpause", container).Run()
		}
	}()
	close(repo.release)
	time.Sleep(12 * time.Second)
	v, err := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
	require.NoError(t, err)
	var state workflows.WebSessionState
	require.NoError(t, v.Get(&state))
	require.Zero(t, state.Completed)
	require.Equal(t, "persisting", state.Status)
	out, err = exec.Command("docker", "unpause", container).CombinedOutput()
	require.NoError(t, err, string(out))
	paused = false
	f.completed(t, cmd)
	events, err = f.repo.ListEvents(f.ctx, c.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "turn.completed", events[0].Type)
	t.Log("actual test Postgres paused through settlement timeout; Temporal completion stayed zero until DB recovered")
}
func TestRealAPITextChat(t *testing.T) {
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b"} {
		t.Run(model, func(t *testing.T) {
			f := setup(t)
			var m llm.Model = mock.New()
			live := os.Getenv("WEBCHAT_TEST_LIVE_MODELS") == "1"
			if live {
				var err error
				m, err = ollama.New("http://127.0.0.1:11434")
				require.NoError(t, err)
			}
			w := f.start(t, f.repo, m)
			hctx, cancel := context.WithCancel(f.ctx)
			done := make(chan struct{})
			go func() { defer close(done); f.gateway.ObserveHealth(hctx) }()
			defer func() { cancel(); <-done }()
			require.Eventually(t, func() bool { return f.gateway.Connected() && f.gateway.WorkersPolling() }, 10*time.Second, 100*time.Millisecond)
			s := httptest.NewUnstartedServer(nil)
			secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			handler, err := api.NewServer(f.repo, api.Config{ListenAddress: s.Listener.Addr().String(), BrowserOrigin: "http://localhost:3000", ProxySecret: secret, GatewayConnected: f.gateway.Connected, WorkersPolling: f.gateway.WorkersPolling, ReconciliationReady: func() bool { return true }})
			require.NoError(t, err)
			s.Config.Handler = handler
			s.Start()
			defer s.Close()
			call := func(method, path, body string, target any) int {
				t.Helper()
				req, err := http.NewRequestWithContext(f.ctx, method, s.URL+path, strings.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("X-Webchat-Secret", secret)
				req.Header.Set("Content-Type", "application/json")
				res, err := s.Client().Do(req)
				require.NoError(t, err)
				defer res.Body.Close()
				if target != nil {
					require.NoError(t, json.NewDecoder(res.Body).Decode(target))
				}
				return res.StatusCode
			}
			var created struct {
				Conversation struct {
					ID         chat.ConversationID `json:"id"`
					WorkflowID string              `json:"workflowId"`
				}
			}
			require.Equal(t, 201, call("POST", "/conversations", fmt.Sprintf(`{"requestId":%q,"model":%q}`, uuid.NewString(), model), &created))
			t.Cleanup(func() {
				f.sdk.TerminateWorkflow(context.Background(), created.Conversation.WorkflowID, "", "test cleanup")
			})
			require.Equal(t, 200, call("GET", "/readyz", "", nil))
			var rc struct {
				TurnID chat.TurnID `json:"turnId"`
			}
			require.Equal(t, 202, call("POST", "/conversations/"+string(created.Conversation.ID)+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"Reply with a short friendly greeting in English."}`, uuid.NewString()), &rc))
			// Dispatch the API's persisted intent, not a separately fabricated command.
			_, err = f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE conversation_id<>$1", created.Conversation.ID)
			require.NoError(t, err)
			d, err := dispatch.New(f.repo, f.gateway, dispatch.Options{})
			require.NoError(t, err)
			worked, err := d.RunOnce(f.ctx)
			require.NoError(t, err)
			require.True(t, worked)
			f.completed(t, chat.SessionCommand{ConversationID: created.Conversation.ID, TurnID: rc.TurnID})
			var history struct{ Messages []struct{ Role, Text string } }
			require.Equal(t, 200, call("GET", "/conversations/"+string(created.Conversation.ID)+"/messages", "", &history))
			require.Len(t, history.Messages, 2)
			require.Equal(t, "assistant", history.Messages[1].Role)
			require.NotEmpty(t, history.Messages[1].Text)
			if live {
				require.NotContains(t, history.Messages[1].Text, "Mock assistant")
			}
			t.Logf("live=%t exact model=%s persisted answer=%q", live, model, history.Messages[1].Text)
			// Restart all worker/client resources, then continue through the same
			// API and durable transcript. No worker memory supplies this context.
			require.NoError(t, w.Close())
			f.start(t, f.repo, m)
			require.Equal(t, 202, call("POST", "/conversations/"+string(created.Conversation.ID)+"/messages", fmt.Sprintf(`{"requestId":%q,"text":"In one sentence, what did I ask you to do in my previous message?"}`, uuid.NewString()), &rc))
			worked, err = d.RunOnce(f.ctx)
			require.NoError(t, err)
			require.True(t, worked)
			f.completed(t, chat.SessionCommand{ConversationID: created.Conversation.ID, TurnID: rc.TurnID})
			require.Equal(t, 200, call("GET", "/conversations/"+string(created.Conversation.ID)+"/messages", "", &history))
			require.Len(t, history.Messages, 4)
			require.Equal(t, "assistant", history.Messages[3].Role)
			require.NotEmpty(t, history.Messages[3].Text)
			t.Logf("live=%t exact model=%s persisted follow-up after worker restart=%q", live, model, history.Messages[3].Text)
		})
	}
}
