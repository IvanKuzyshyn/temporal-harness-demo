//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	tc "go.temporal.io/sdk/client"
)

type approvalModel struct{ calls atomic.Int32 }

func (m *approvalModel) Chat(ctx context.Context, r llm.Request, _ func(llm.Delta) error) (llm.Response, error) {
	m.calls.Add(1)
	if len(r.Tools) != 2 || r.Tools[0].Name != "web_fetch" || r.Tools[1].Name != "file_upload" {
		return llm.Response{}, fmt.Errorf("wrong web toolset")
	}
	for _, msg := range r.Messages {
		if msg.Role == "tool" {
			return llm.Response{Message: llm.Message{Role: "assistant", Content: "Recorded tool outcomes explain this answer."}}, nil
		}
	}
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "provider-one", Name: "web_fetch", Args: json.RawMessage(`{"url":"https://example.com/one"}`)}, {ID: "provider-two", Name: "web_fetch", Args: json.RawMessage(`{"url":"https://example.com/two"}`)}}}}, nil
}
func (f *fixture) approvalState(t *testing.T, c chat.Conversation) workflows.WebSessionState {
	t.Helper()
	v, err := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
	require.NoError(t, err)
	var s workflows.WebSessionState
	require.NoError(t, v.Get(&s))
	return s
}
func (f *fixture) pending(t *testing.T, c chat.Conversation) *chat.WebApproval {
	t.Helper()
	var p *chat.WebApproval
	require.Eventually(t, func() bool {
		v, e := f.sdk.QueryWorkflow(f.ctx, c.WorkflowID, "", "state")
		if e != nil {
			return false
		}
		var s workflows.WebSessionState
		if v.Get(&s) != nil {
			return false
		}
		p = s.PendingApproval
		return p != nil && p.Decision == ""
	}, 15*time.Second, 20*time.Millisecond)
	return p
}

// Builds and starts the actual API executable. Each stop joins exactly its own
// child process; it never signals an ambient API or changes service settings.
func approvalAPI(t *testing.T, f *fixture) (func(), func(string, any, int) []byte) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "web-api")
	build := exec.CommandContext(f.ctx, "go", "build", "-p", "1", "-o", binary, "./cmd/web-api")
	build.Dir = "../.."
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := l.Addr().String()
	require.NoError(t, l.Close())
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var process *exec.Cmd
	var log *os.File
	stop := func() {
		if process == nil {
			return
		}
		require.NoError(t, process.Process.Signal(os.Interrupt))
		require.NoError(t, process.Wait())
		process = nil
		log.Close()
	}
	start := func() {
		log, err = os.Create(filepath.Join(dir, "api.log"))
		require.NoError(t, err)
		process = exec.Command(binary)
		process.Env = append(os.Environ(), "WEBCHAT_DATABASE_URL="+os.Getenv("WEBCHAT_TEST_DATABASE_URL"), "WEBCHAT_API_LISTEN="+address, "WEBCHAT_BROWSER_ORIGIN=http://localhost:3000", "WEBCHAT_PROXY_SECRET="+secret, "WEBCHAT_TEMPORAL_ADDRESS="+f.address, "WEBCHAT_EXECUTION_BACKEND=")
		process.Stdout = log
		process.Stderr = log
		require.NoError(t, process.Start())
		require.Eventually(t, func() bool {
			req, _ := http.NewRequest("GET", "http://"+address+"/healthz", nil)
			req.Header.Set("X-Webchat-Secret", secret)
			resp, e := http.DefaultClient.Do(req)
			if e != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == 200
		}, 15*time.Second, 50*time.Millisecond)
	}
	start()
	t.Cleanup(stop)
	request := func(path string, body any, status int) []byte {
		method := "GET"
		var raw []byte
		if body != nil {
			method = "POST"
			raw, _ = json.Marshal(body)
		}
		req, err := http.NewRequestWithContext(f.ctx, method, "http://"+address+path, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("X-Webchat-Secret", secret)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var value json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&value))
		require.Equal(t, status, resp.StatusCode, string(value))
		return value
	}
	return func() { stop(); start() }, request
}

func (f *fixture) approvalCommand(t *testing.T, c chat.Conversation, p *chat.WebApproval, decision chat.ApprovalDecision) chat.SessionCommand {
	t.Helper()
	// Workflow pending state precedes RecordWebApproval's SQL commit. Real UI
	// consent is offered from that durable projection, not the workflow query.
	require.Eventually(t, func() bool {
		var exists bool
		e := f.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM approvals WHERE conversation_id=$1 AND id=$2 AND action_hash=$3)`, c.ID, p.ID, p.Scope.ActionHash).Scan(&exists)
		return e == nil && exists
	}, 10*time.Second, 10*time.Millisecond)
	in := chat.CommandInput{ConversationID: c.ID, RequestID: uuid.NewString(), Kind: chat.CommandDecide, Payload: chat.CommandPayload{ApprovalID: p.ID, ActionHash: p.Scope.ActionHash, Decision: decision}}
	rc, err := f.repo.AcceptCommand(f.ctx, in)
	require.NoError(t, err)
	var seq int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT sequence FROM commands WHERE id=$1`, rc.CommandID).Scan(&seq))
	return chat.SessionCommand{CommandID: rc.CommandID, ConversationID: c.ID, RequestID: in.RequestID, WorkflowID: c.WorkflowID, Model: c.Model, Kind: in.Kind, Payload: in.Payload, PayloadDigest: rc.PayloadDigest, Sequence: seq}
}
func TestStaleDecisionAfterReplacement(t *testing.T) {
	f := setup(t)
	f.start(t, f.repo, &approvalModel{})
	c := f.conversation(t, "qwen3.5:9b")
	first := f.command(t, c, "await approval")
	_, err := f.gateway.Submit(f.ctx, first)
	require.NoError(t, err)
	old := f.pending(t, c)
	replacement := f.control(t, c, first.TurnID, chat.CommandReplace, "replace pending action")
	accepted, err := f.gateway.Submit(f.ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, "accepted", accepted.State)
	require.Eventually(t, func() bool {
		p, e := f.repo.GetTurn(f.ctx, chat.TurnRef{ConversationID: c.ID, TurnID: first.TurnID})
		return e == nil && p.Status == "cancelled"
	}, 15*time.Second, 20*time.Millisecond)
	current := f.pending(t, c)
	require.NotEqual(t, old.ID, current.ID)
	stale := f.approvalCommand(t, c, old, chat.ApprovalAll)
	result, err := f.gateway.Submit(f.ctx, stale)
	require.NoError(t, err)
	require.Equal(t, "rejected", result.State)
	require.Equal(t, "invalid_approval", result.Code)
	retry, err := f.gateway.Submit(f.ctx, stale)
	require.NoError(t, err)
	require.Equal(t, result, retry)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM web_tool_invocations WHERE conversation_id=$1`, c.ID).Scan(&count))
	require.Zero(t, count)
	var outcome string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT outcome FROM approvals WHERE id=$1`, old.ID).Scan(&outcome))
	require.Equal(t, "cancelled", outcome)
	require.Empty(t, f.approvalState(t, c).Policy.GrantedToolset)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM tool_calls WHERE turn_id=$1 AND status<>'cancelled'`, first.TurnID).Scan(&count))
	require.Zero(t, count, "cancelled turn cannot leave pending/running tool projections")
}

func TestApproveAllSurvivesRollover(t *testing.T) {
	f := setup(t)
	c := f.conversation(t, "qwen3.5:9b")
	model := &approvalModel{}
	var effects atomic.Int32
	registry := tools.NewWebRegistry(map[string]tools.WebAdapter{"web_fetch": func(ctx context.Context, s chat.ApprovalScope, args json.RawMessage) (chat.WebToolResult, error) {
		effects.Add(1)
		return chat.WebToolResult{Text: "test transport result"}, nil
	}})
	start := func() *worker.Runner {
		r, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: model, WebTools: registry})
		require.NoError(t, e)
		return r
	}
	runner := start()
	t.Cleanup(func() { runner.Close() })
	run, err := f.sdk.ExecuteWorkflow(f.ctx, tc.StartWorkflowOptions{ID: c.WorkflowID, TaskQueue: workflows.WebAgentsQueue}, workflows.WebSessionName, workflows.WebSessionInput{ConversationID: c.ID, Model: c.Model, ContinueAfter: 2})
	require.NoError(t, err)
	// Keep this owned database's earlier fixtures out of the API dispatch lane.
	_, err = f.pool.Exec(f.ctx, `UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE conversation_id<>$1`, c.ID)
	require.NoError(t, err)
	restartAPI, httpCall := approvalAPI(t, f)
	base := "/conversations/" + string(c.ID)
	message := func(text string) chat.SessionCommand {
		raw := httpCall(base+"/messages", map[string]any{"requestId": uuid.NewString(), "text": text}, 202)
		var receipt chat.CommandReceipt
		require.NoError(t, json.Unmarshal(raw, &receipt))
		return chat.SessionCommand{ConversationID: c.ID, TurnID: receipt.TurnID, CommandID: receipt.CommandID}
	}
	decide := func(p *chat.WebApproval, choice string) {
		raw := httpCall(base+"/approvals/"+p.ID+"/decision", map[string]any{"requestId": uuid.NewString(), "actionHash": p.Scope.ActionHash, "decision": choice}, 202)
		var receipt chat.CommandReceipt
		require.NoError(t, json.Unmarshal(raw, &receipt))
		require.Eventually(t, func() bool {
			r, e := f.repo.GetCommand(f.ctx, c.ID, receipt.CommandID)
			return e == nil && r.ResultState == "accepted"
		}, 15*time.Second, 20*time.Millisecond)
	}
	first := message("two approved actions")
	pending := f.pending(t, c)
	require.Zero(t, effects.Load())
	require.NoError(t, runner.Close())
	restartAPI()
	runner = start()
	restored := f.pending(t, c)
	require.Equal(t, pending, restored, "pending scope/deadline survive actual API/worker restart")
	metadata := httpCall(base, nil, 200)
	require.Contains(t, string(metadata), pending.ID)
	decide(restored, "once")
	second := f.pending(t, c)
	require.NotEqual(t, pending.ID, second.ID)
	require.Zero(t, effects.Load(), "authorize whole batch in order before bounded parallel execution")
	decide(second, "all")
	f.completed(t, first)
	require.EqualValues(t, 2, effects.Load())
	require.NoError(t, runner.Close())
	restartAPI()
	runner = start()
	require.Equal(t, chat.WebToolset, f.approvalState(t, c).Policy.GrantedToolset)
	metadata = httpCall(base, nil, 200)
	require.Contains(t, string(metadata), `"approvalAll":true`)
	next := message("all policy after restart and rollover")
	f.completed(t, next)
	require.EqualValues(t, 4, effects.Load())
	// Reusing recorded results as subsequent model context caused no reexecution.
	require.EqualValues(t, 4, model.calls.Load())
	raw := httpCall(base+"/approval-policy/revoke", map[string]any{"requestId": uuid.NewString()}, 202)
	var rc chat.CommandReceipt
	require.NoError(t, json.Unmarshal(raw, &rc))
	require.Eventually(t, func() bool {
		r, e := f.repo.GetCommand(f.ctx, c.ID, rc.CommandID)
		return e == nil && r.ResultState == "accepted"
	}, 15*time.Second, 20*time.Millisecond)
	denied := message("new work after revoke")
	p := f.pending(t, c)
	require.EqualValues(t, 4, effects.Load())
	decide(p, "deny")
	p = f.pending(t, c)
	decide(p, "deny")
	f.completed(t, denied)
	require.EqualValues(t, 4, effects.Load())
	var decisions, authorized int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM approvals WHERE conversation_id=$1 AND decision IS NOT NULL AND outcome IS NOT NULL`, c.ID).Scan(&decisions))
	require.Equal(t, 4, decisions)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM tool_calls WHERE conversation_id=$1 AND authorized_scope IS NOT NULL AND status='completed'`, c.ID).Scan(&authorized))
	require.Equal(t, 4, authorized)
	rollovers := 0
	id := run.GetRunID()
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
		}
		if next == "" {
			break
		}
		id = next
	}
	require.GreaterOrEqual(t, rollovers, 2)
	t.Logf("real API/worker restarts while pending and all-enabled; %d actual CAN boundaries, %d tool transport side effects", rollovers, effects.Load())
}
