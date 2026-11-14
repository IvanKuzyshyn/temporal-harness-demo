//go:build integration

package webintegration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	tw "go.temporal.io/sdk/worker"
)

type fetchBatchModel struct {
	mu       sync.Mutex
	contexts [][]chat.WebToolResult
}

func (m *fetchBatchModel) Chat(_ context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	var outcomes []chat.WebToolResult
	for _, msg := range r.Messages {
		if msg.Role == "tool" {
			var result chat.WebToolResult
			if strings.HasPrefix(msg.Content, "Tool outcome:") {
				result.Error = strings.TrimSpace(strings.TrimPrefix(msg.Content, "Tool outcome:"))
			} else if err := json.Unmarshal([]byte(msg.Content), &result); err != nil {
				return llm.Response{}, err
			}
			outcomes = append(outcomes, result)
		}
	}
	if len(outcomes) > 0 {
		m.mu.Lock()
		m.contexts = append(m.contexts, outcomes)
		m.mu.Unlock()
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Answer from recorded approved sources."}}, nil
	}
	var calls []llm.ToolCall
	for i := 0; i < 4; i++ {
		calls = append(calls, llm.ToolCall{ID: fmt.Sprint(i), Name: "web_fetch", Args: json.RawMessage(fmt.Sprintf(`{"url":"http://source.example/%d"}`, i))})
	}
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: calls}}, nil
}

// Inject a lost SQL acknowledgement only AFTER the real canonical result commit.
// The retried real activity must adopt it without another fetch.
type fetchLostAck struct {
	*storage.Repository
	mu       sync.Mutex
	failed   map[string]bool
	failures atomic.Int32
}

func (r *fetchLostAck) SaveWebToolResult(ctx context.Context, s chat.ApprovalScope, id string, result chat.WebToolResult) error {
	if err := r.Repository.SaveWebToolResult(ctx, s, id, result); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.failed[s.ToolCallID] {
		r.failed[s.ToolCallID] = true
		r.failures.Add(1)
		return errors.New("injected fetch result lost acknowledgment")
	}
	return nil
}
func TestFetchReplacementJoinsPhysicalDial(t *testing.T) {
	f := setup(t)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	fetcher := tools.NewFetcherWithNetwork(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil, ctx.Err()
	})
	a := &activities.ChatActivities{Repository: f.repo, Model: oneToolModel{}, WebTools: tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": activities.NewWebFetchAdapter(fetcher)})}
	agents := tw.New(f.sdk, workflows.WebAgentsQueue, tw.Options{MaxHeartbeatThrottleInterval: 100 * time.Millisecond, DefaultHeartbeatThrottleInterval: 100 * time.Millisecond})
	agents.RegisterWorkflow(workflows.WebChatSessionWorkflow)
	agents.RegisterActivity(a)
	require.NoError(t, agents.Start())
	models := tw.New(f.sdk, workflows.WebLLMQueue, tw.Options{})
	models.RegisterActivityWithOptions(a.ChatStep, activity.RegisterOptions{Name: workflows.WebModelActivity})
	require.NoError(t, models.Start())
	defer func() { unblock(); models.Stop(); agents.Stop() }()
	c := f.conversation(t, "qwen3.5:9b")
	cmd := f.command(t, c, "held fetch dial")
	_, err := f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	pending := f.pending(t, c)
	_, err = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalOnce))
	require.NoError(t, err)
	waitBarrier(t, entered)
	replacement := f.control(t, c, cmd.TurnID, chat.CommandReplace, "replacement waits for physical fetch work")
	accepted, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, "accepted", accepted.State)
	waitBarrier(t, cancelled)
	var missing int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL`, cmd.TurnID).Scan(&missing))
	require.Equal(t, 1, missing)
	target, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
	require.NoError(t, err)
	require.Equal(t, "cancelling", target.Status)
	next, err := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: replacement.TurnID})
	require.NoError(t, err)
	require.Equal(t, "queued", next.Status)
	unblock()
	require.Eventually(t, func() bool {
		target, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: cmd.TurnID})
		return e == nil && target.Status == "cancelled"
	}, 15*time.Second, 20*time.Millisecond)
	nextApproval := f.pending(t, c)
	require.NotEqual(t, pending.ID, nextApproval.ID)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL`, cmd.TurnID).Scan(&missing))
	require.Zero(t, missing)
	_, err = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, nextApproval, chat.ApprovalDeny))
	require.NoError(t, err)
	f.completed(t, replacement)
	t.Log("accepted replacement remained queued with one missing fetch cleanup ACK until the physical dial returned")
}

func TestFetchBatchActuallyParallel(t *testing.T) {
	f := setup(t)
	model := &fetchBatchModel{}
	repo := &fetchLostAck{Repository: f.repo, failed: map[string]bool{}}
	var requests, active, peak, lookups atomic.Int32
	entered := make(chan string, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var wires sync.WaitGroup
	fetcher := tools.NewFetcherWithNetwork(func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:80" {
			return nil, fmt.Errorf("unpinned address %s", address)
		}
		client, server := net.Pipe()
		wires.Add(1)
		go func() {
			defer wires.Done()
			defer server.Close()
			r, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			defer r.Body.Close()
			requests.Add(1)
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			entered <- r.URL.Path
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			body := "evidence for " + r.URL.Path
			fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len("<p>"+body+"</p>"), "<p>"+body+"</p>")
		}()
		return client, nil
	})
	registry := tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": activities.NewWebFetchAdapter(fetcher)})
	runner, err := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: repo, Model: model, WebTools: registry})
	require.NoError(t, err)
	defer func() { unblock(); runner.Close(); wires.Wait() }()
	c := f.conversation(t, "qwen3.5:9b")
	cmd := f.command(t, c, "four independent sources")
	_, err = f.gateway.Submit(f.ctx, cmd)
	require.NoError(t, err)
	var scopes []chat.ApprovalScope
	for i := 0; i < 4; i++ {
		var p *chat.WebApproval
		require.Eventually(t, func() bool {
			p = f.approvalState(t, c).PendingApproval
			return p != nil && p.Decision == "" && (i == 0 || p.Scope.ToolCallID != scopes[i-1].ToolCallID)
		}, 15*time.Second, 20*time.Millisecond)
		var raw []byte
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT arguments FROM tool_calls WHERE id=$1`, p.Scope.ToolCallID).Scan(&raw))
		var args tools.FetchArgs
		require.NoError(t, json.Unmarshal(raw, &args))
		require.Equal(t, fmt.Sprintf("http://source.example/%d", i), args.URL)
		require.Zero(t, requests.Load(), "all deterministic preflight decisions precede any I/O")
		require.Zero(t, lookups.Load(), "approval preflight must not resolve DNS")
		scopes = append(scopes, p.Scope)
		result, e := f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalOnce))
		require.NoError(t, e)
		require.Equal(t, "accepted", result.State)
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		select {
		case path := <-entered:
			seen[path] = true
		case <-time.After(8 * time.Second):
			t.Fatalf("only %d actual requests overlapped", len(seen))
		}
	}
	require.Len(t, seen, 4)
	require.EqualValues(t, 4, active.Load())
	require.EqualValues(t, 4, peak.Load())
	unblock()
	f.completed(t, cmd)
	require.EqualValues(t, 4, requests.Load())
	require.EqualValues(t, 4, repo.failures.Load())
	var invocations, acked int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*),count(cleanup_confirmed_at) FROM web_tool_invocations WHERE turn_id=$1`, cmd.TurnID).Scan(&invocations, &acked))
	require.Equal(t, 8, invocations)
	require.Equal(t, invocations, acked)
	for i, s := range scopes {
		r, e := f.repo.WebToolResult(f.ctx, c.ID, s.ToolCallID)
		require.NoError(t, e)
		require.Equal(t, fmt.Sprintf("evidence for /%d", i), r.Text)
		var raw map[string]any
		b, _ := json.Marshal(r)
		require.NoError(t, json.Unmarshal(b, &raw))
		require.Equal(t, fmt.Sprintf("http://source.example/%d", i), raw["requestedUrl"])
		require.Equal(t, raw["requestedUrl"], raw["finalUrl"])
		require.Equal(t, false, raw["truncated"])
	}
	model.mu.Lock()
	require.Len(t, model.contexts, 1)
	require.Len(t, model.contexts[0], 4)
	for i, r := range model.contexts[0] {
		require.Equal(t, fmt.Sprintf("evidence for /%d", i), r.Text)
	}
	model.mu.Unlock()
	// A separate denied batch reaches the model as recorded denial, never DNS/HTTP.
	denied := f.command(t, c, "deny four new fetches")
	_, err = f.gateway.Submit(f.ctx, denied)
	require.NoError(t, err)
	previous := ""
	for i := 0; i < 4; i++ {
		var p *chat.WebApproval
		require.Eventually(t, func() bool {
			p = f.approvalState(t, c).PendingApproval
			return p != nil && p.Decision == "" && p.ID != previous
		}, 15*time.Second, 20*time.Millisecond)
		previous = p.ID
		_, err = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, p, chat.ApprovalDeny))
		require.NoError(t, err)
	}
	f.completed(t, denied)
	require.EqualValues(t, 4, requests.Load())
	require.EqualValues(t, 4, lookups.Load(), "cached and denied work must not even resolve DNS")
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1`, denied.TurnID).Scan(&invocations))
	require.Zero(t, invocations)
	model.mu.Lock()
	defer model.mu.Unlock()
	require.Len(t, model.contexts, 2)
	for _, r := range model.contexts[1] {
		require.Equal(t, "denied", r.Error)
	}
	t.Log("four real HTTP requests overlapped; deterministic preflight, eight durable activity bodies/four network effects after lost acknowledgments; denied batch zero invocations")
}
