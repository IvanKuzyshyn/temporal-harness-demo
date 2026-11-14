//go:build integration

package api_test

// Reviewer-only real API/SQL transport with explicitly synthetic saved states.
// No dispatcher, worker, provider, remote fetch or inference is started. Commands
// are durably queued, never misrepresented as executed. Attachments use the real
// bounded store/parser. Stop by creating <dir>/done; all data stays in <dir>/DB.
import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSimplificationBrowserRuntime(t *testing.T) {
	dir := os.Getenv("WEBCHAT_SIMPLIFICATION_BROWSER_DIR")
	if dir == "" {
		t.Skip("opt-in zero-inference reviewer fixture")
	}
	require.True(t, filepath.IsAbs(dir))
	require.NoError(t, os.MkdirAll(dir, 0700))
	r, p := database(t)
	ctx := context.Background()
	store, err := attachments.NewStore(filepath.Join(dir, "attachments"), r, nil)
	require.NoError(t, err)
	defer store.Close()
	fixtures := map[string]string{}
	for _, kind := range []string{"empty", "populated", "approval", "error", "tool-failure", "attachment-approval"} {
		c, err := r.CreateConversation(ctx, "qwen3.5:9b")
		require.NoError(t, err)
		fixtures[kind] = string(c.ID)
		if kind == "empty" {
			continue
		}
		if kind == "attachment-approval" {
			var older chat.Attachment
			for i, text := range []string{"First document: original budget. Synthetic fixture.", "Second document: revised budget. Please compare with the first. Synthetic fixture."} {
				a, e := store.Put(ctx, c.ID, strings.NewReader(text), attachments.UploadMetadata{Name: "report.txt", MediaType: "text/plain"})
				require.NoError(t, e)
				if i == 0 {
					older = a
				}
				cmd, e := r.AcceptCommand(ctx, chat.CommandInput{RequestID: uuid.NewString(), ConversationID: c.ID, Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: text, AttachmentIDs: []chat.AttachmentID{a.ID}}})
				require.NoError(t, e)
				require.NoError(t, r.RecordCommandResult(ctx, c.ID, cmd.CommandID, "dispatched", "accepted", []byte(`{"state":"accepted"}`)))
				if i == 0 {
					_, e = p.Exec(ctx, "UPDATE turns SET status='completed' WHERE id=$1", cmd.TurnID)
				} else {
					args, _ := json.Marshal(map[string]string{"attachmentId": string(older.ID), "instruction": "Summarize the original budget before comparing it with the revision."})
					tool := chat.ToolCall{ID: uuid.NewString(), ConversationID: c.ID, TurnID: cmd.TurnID, LogicalKey: "review/older-attachment", Name: "file_upload", Arguments: args, ActionHash: strings.Repeat("b", 64), ParentWorkflowID: c.WorkflowID, Status: "pending"}
					require.NoError(t, r.RecordToolCall(ctx, tool))
					require.NoError(t, r.RecordApproval(ctx, chat.Approval{ID: uuid.NewString(), ConversationID: c.ID, ToolCallID: tool.ID, ActionHash: tool.ActionHash, Deadline: time.Now().Add(time.Hour)}))
					_, e = p.Exec(ctx, "UPDATE turns SET status='awaiting_approval' WHERE id=$1", cmd.TurnID)
				}
				require.NoError(t, e)
			}
			continue
		}
		text := "How can I make a small project easier to maintain?"
		if kind == "approval" {
			text = "Read this page and summarize the useful parts."
		}
		if kind == "error" {
			text = "Show a concise failure without execution diagnostics."
		}
		cmd, err := r.AcceptCommand(ctx, chat.CommandInput{RequestID: uuid.NewString(), ConversationID: c.ID, Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: text}})
		require.NoError(t, err)
		require.NoError(t, r.RecordCommandResult(ctx, c.ID, cmd.CommandID, "dispatched", "accepted", []byte(`{"state":"accepted"}`)))
		switch kind {
		case "tool-failure":
			require.NoError(t, r.RecordToolCall(ctx, chat.ToolCall{ID: uuid.NewString(), ConversationID: c.ID, TurnID: cmd.TurnID, LogicalKey: "review/failed-tool", Name: "web_fetch", Arguments: []byte(`{"url":"https://example.com/article"}`), ActionHash: strings.Repeat("c", 64), ParentWorkflowID: c.WorkflowID, Status: "failed", Result: []byte(`{"error":"tool_failed"}`)}))
			require.NoError(t, r.AppendMessage(ctx, chat.MessageRecord{EntryID: uuid.NewString(), ConversationID: c.ID, TurnID: cmd.TurnID, Role: "assistant", Text: "Keep changes small and test their boundaries. This completed answer and failed web operation are synthetic saved records, not provider output.", LogicalStep: 1, ResultKind: "response", CompletionState: "completed"}))
			_, err = p.Exec(ctx, "UPDATE turns SET status='completed' WHERE id=$1", cmd.TurnID)
		case "populated":
			require.NoError(t, r.AppendMessage(ctx, chat.MessageRecord{EntryID: uuid.NewString(), ConversationID: c.ID, TurnID: cmd.TurnID, Role: "assistant", Text: "Start with a few simple habits:\n\n1. **Keep the scope small.** Give each change one clear purpose.\n2. **Test the boundaries.** Cover the inputs that are easiest to get wrong.\n3. **Write down decisions.** Explain why, not just how.\n\nA short README and a reliable test command are a useful starting point.\n\n```sh\ngo test -p 1 ./...\n```\n\nThis is a synthetic review fixture, not a model response.", LogicalStep: 1, ResultKind: "response", CompletionState: "completed"}))
			_, err = p.Exec(ctx, "UPDATE turns SET status='completed' WHERE id=$1", cmd.TurnID)
		case "error":
			_, err = p.Exec(ctx, "UPDATE turns SET status='failed',reason='fixture_provider_error' WHERE id=$1", cmd.TurnID)
		case "approval":
			tool := chat.ToolCall{ID: uuid.NewString(), ConversationID: c.ID, TurnID: cmd.TurnID, LogicalKey: "review/tool", Name: "web_fetch", Arguments: []byte(`{"url":"https://example.com/article"}`), ActionHash: strings.Repeat("a", 64), ParentWorkflowID: c.WorkflowID, Status: "pending"}
			require.NoError(t, r.RecordToolCall(ctx, tool))
			require.NoError(t, r.RecordApproval(ctx, chat.Approval{ID: uuid.NewString(), ConversationID: c.ID, ToolCallID: tool.ID, ActionHash: tool.ActionHash, Deadline: time.Now().Add(time.Hour)}))
			_, err = p.Exec(ctx, "UPDATE turns SET status='awaiting_approval' WHERE id=$1", cmd.TurnID)
		}
		require.NoError(t, err)
	}
	apiListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	webListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	webAddr := webListener.Addr().String()
	require.NoError(t, webListener.Close())
	origin := "http://" + webAddr
	key := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	handler, err := api.NewServer(r, api.Config{ListenAddress: apiListener.Addr().String(), ProxySecret: key, BrowserOrigin: origin, Attachments: store})
	require.NoError(t, err)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(apiListener)
	defer server.Close()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(webAddr)
	cmd := exec.Command("node", filepath.Join(root, "web/node_modules/next/dist/bin/next"), "start", "--hostname", "127.0.0.1", "--port", port)
	cmd.Dir = filepath.Join(root, "web")
	cmd.Env = append(os.Environ(), "WEBCHAT_API_URL=http://"+apiListener.Addr().String(), "WEBCHAT_BROWSER_ORIGIN="+origin, "WEBCHAT_PROXY_SECRET="+key)
	log, err := os.OpenFile(filepath.Join(dir, "next.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	require.NoError(t, cmd.Start())
	defer func() {
		_ = server.Close()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: time.Second}
	require.Eventually(t, func() bool {
		resp, e := client.Get(origin)
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, 30*time.Second, 200*time.Millisecond)
	ready, _ := json.MarshalIndent(map[string]any{"url": origin, "apiAddress": apiListener.Addr().String(), "goTestPID": os.Getpid(), "nextPID": cmd.Process.Pid, "fixtures": fixtures, "mode": "real API/SQL + synthetic saved projections; NO inference/dispatcher; new commands queue only", "stop": "create done in this directory"}, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready.json"), ready, 0600))
	t.Log(string(ready))
	deadline := time.After(15 * time.Minute)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("review fixture expired after 15 minutes")
		case <-tick.C:
			if _, e := os.Stat(filepath.Join(dir, "done")); e == nil {
				return
			}
		}
	}
}
