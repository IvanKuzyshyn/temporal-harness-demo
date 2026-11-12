//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	modelcontext "github.com/IvanKuzyshyn/temporal-harness-demo/internal/context"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Read-only host RSS samples, NOT total GPU memory or a hard resource cap.
func providerRSS(ctx context.Context) int64 {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, e := exec.CommandContext(ctx, "ps", "-axo", "pid=,rss=,comm=").Output()
	if e != nil {
		return -1
	}
	var total int64
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		name := filepath.Base(strings.Join(f[2:], " "))
		if name == "ollama" || name == "ollama-runner" {
			n, e := strconv.ParseInt(f[1], 10, 64)
			if e == nil {
				total += n * 1024
			}
		}
	}
	return total
}

type contextLiveModel struct {
	base llm.Model
	file *os.File
	mu   sync.Mutex
}

func (m *contextLiveModel) record(v any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := json.NewEncoder(m.file).Encode(v); e != nil {
		return e
	}
	return m.file.Sync()
}
func (m *contextLiveModel) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	admitted, e := modelcontext.Assemble(req, nil, modelcontext.DefaultBudget())
	if e != nil {
		return llm.Response{}, e
	}
	phase := "conversation"
	if req.Image != nil {
		phase = "image"
	} else if strings.Contains(req.Messages[0].Content, "Summarize the following completed") {
		phase = "summary"
	} else if len(req.Tools) == 0 {
		phase = "document"
	}
	start := time.Now()
	before := providerRSS(ctx)
	peak := before
	sample, stop := context.WithCancel(ctx)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-sample.Done():
				return
			case <-tick.C:
				n := providerRSS(sample)
				if n > peak {
					peak = n
				}
			}
		}
	}()
	res, err := m.base.Chat(ctx, req, emit)
	stop()
	<-joined
	after := providerRSS(ctx)
	if after > peak {
		peak = after
	}
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	recordErr := m.record(map[string]any{"kind": "provider_call", "model": req.Model, "phase": phase, "latencyMs": time.Since(start).Milliseconds(), "accounting": admitted.Accounting, "transportSucceeded": err == nil, "error": errorText, "output": res.Message.Content, "tokens": res.Tokens, "doneReason": res.DoneReason, "outputExhausted": res.OutputExhausted(req.MaxOutputTokens), "hostRSSBytesBefore": before, "hostRSSBytesAfter": after, "hostRSSBytesSampledPeak": peak, "resourceLimit": "read-only process RSS samples; not GPU admission or reclamation proof", "imageAdmissionQualified": false})
	if err == nil {
		err = recordErr
	}
	return res, err
}
func TestContextLiveModels(t *testing.T) {
	if os.Getenv("WEBCHAT_TASK10_LIVE") != "1" {
		t.Skip("explicit isolated live-model opt-in")
	}
	path := os.Getenv("WEBCHAT_TASK10_EVIDENCE")
	require.NotEmpty(t, path)
	file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, e)
	defer file.Close()
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b"} {
		t.Run(model, func(t *testing.T) {
			f := setup(t)
			root := t.TempDir()
			store, e := attachments.NewStore(root, f.repo, nil)
			require.NoError(t, e)
			defer func() { store.Close() }()
			provider, e := ollama.New("http://127.0.0.1:11434")
			require.NoError(t, e)
			m := &contextLiveModel{base: provider, file: file}
			start := func() *worker.Runner {
				r, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store})
				require.NoError(t, e)
				return r
			}
			runner := start()
			defer func() { runner.Close() }()
			c := f.conversation(t, model)
			send := func(text string) chat.SessionCommand {
				cmd := f.command(t, c, text)
				_, e := f.gateway.Submit(f.ctx, cmd)
				require.NoError(t, e)
				f.completed(t, cmd)
				return cmd
			}
			send("My secret phrase is blue kite. Remember it. Reply only Noted. Do not use tools.")
			analyze := func(name, media, instruction string, data []byte) chat.WebToolResult {
				att, e := store.Put(f.ctx, c.ID, bytes.NewReader(data), attachments.UploadMetadata{Name: name, MediaType: media})
				require.NoError(t, e)
				input := chat.CommandInput{ConversationID: c.ID, RequestID: uuid.NewString(), Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: instruction, AttachmentIDs: []chat.AttachmentID{att.ID}}}
				receipt, e := f.repo.AcceptCommand(f.ctx, input)
				require.NoError(t, e)
				var seq int64
				require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT sequence FROM commands WHERE id=$1", receipt.CommandID).Scan(&seq))
				cmd := chat.SessionCommand{CommandID: receipt.CommandID, ConversationID: c.ID, TurnID: receipt.TurnID, RequestID: input.RequestID, Kind: input.Kind, Payload: input.Payload, PayloadDigest: receipt.PayloadDigest, Sequence: seq, WorkflowID: c.WorkflowID, Model: model}
				_, e = f.gateway.Submit(f.ctx, cmd)
				require.NoError(t, e)
				pending := f.pending(t, c)
				_, e = f.gateway.Submit(f.ctx, f.approvalCommand(t, c, pending, chat.ApprovalOnce))
				require.NoError(t, e)
				f.completed(t, cmd)
				result, e := f.repo.WebToolResult(f.ctx, c.ID, pending.Scope.ToolCallID)
				require.NoError(t, e)
				return result
			}
			doc := analyze("project.txt", "text/plain", "State the project code in this document briefly.", []byte("The project code is ORCHID-27. This is a tiny test document."))
			require.NoError(t, m.record(map[string]any{"kind": "document_semantics", "model": model, "semanticAccepted": strings.Contains(doc.Text, "ORCHID-27"), "result": doc}))
			png, e := os.ReadFile("../../scripts/testdata/bonjour.png")
			require.NoError(t, e)
			image := analyze("bonjour.png", "image/png", "Translate the visible French text into English. Return only the translation.", png)
			imageOK := strings.Contains(strings.ToLower(image.Text), "hello") && strings.Contains(strings.ToLower(image.Text), "world")
			require.NoError(t, m.record(map[string]any{"kind": "image_semantics", "model": model, "semanticAccepted": imageOK, "result": image, "imageAdmissionQualified": false}))
			for i := 0; i < 7; i++ {
				send(fmt.Sprintf("This is filler exchange %d. Reply only OK, no tools.", i+1))
			}
			require.NoError(t, runner.Close())
			require.NoError(t, store.Close())
			store, e = attachments.NewStore(root, f.repo, nil)
			require.NoError(t, e)
			runner = start()
			last := send("Using only remembered conversation facts, give my secret phrase, the project code from the earlier document, and the English translation from the earlier image. No tools or new analysis.")
			var answer string
			require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT text FROM messages WHERE turn_id=$1 AND result_kind='final'", last.TurnID).Scan(&answer))
			s, e := f.repo.LatestSummary(f.ctx, c.ID)
			require.NoError(t, e)
			semantic := strings.Contains(strings.ToLower(answer), "blue kite") && strings.Contains(answer, "ORCHID-27") && strings.Contains(strings.ToLower(answer), "hello") && strings.Contains(strings.ToLower(answer), "world")
			require.NoError(t, m.record(map[string]any{"kind": "memory_semantics_after_worker_store_restart", "model": model, "semanticAccepted": semantic, "answer": answer, "summary": s, "imageAdmissionQualified": false}))
			require.True(t, strings.Contains(doc.Text, "ORCHID-27"), "new document semantic failure; evidence retained")
			require.True(t, imageOK, "narrow safe fixture semantic failure; no fallback")
			require.True(t, semantic, "memory semantic failure is not a transport pass")
		})
	}
}
