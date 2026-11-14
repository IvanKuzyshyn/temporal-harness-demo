//go:build integration

package webintegration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/api"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/attachments"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/dispatch"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
)

type tracingImageModel struct {
	base          llm.Model
	images, calls atomic.Int32
}

func (m *tracingImageModel) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	m.calls.Add(1)
	if req.Image != nil {
		m.images.Add(1)
	}
	return m.base.Chat(ctx, req, emit)
}
func imageFixtures(t *testing.T) map[string][]byte {
	pngData, e := os.ReadFile("../../scripts/testdata/bonjour.png")
	require.NoError(t, e)
	img, _, e := image.Decode(bytes.NewReader(pngData))
	require.NoError(t, e)
	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 95}))
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="640" height="160"><rect width="640" height="160" fill="white"/>`)
	dark := func(x, y int) bool { r, g, b, _ := img.At(x, y).RGBA(); return r+g+b < 3*32768 }
	for y := 0; y < 160; y++ {
		for x := 0; x < 640; {
			if !dark(x, y) {
				x++
				continue
			}
			start := x
			for x < 640 && dark(x, y) {
				x++
			}
			fmt.Fprintf(&svg, `<rect x="%d" y="%d" width="%d" height="1"/>`, start, y, x-start)
		}
	}
	svg.WriteString(`</svg>`)
	return map[string][]byte{"png": pngData, "jpg": jpg.Bytes(), "svg": []byte(svg.String())}
}

// Without the opt-in ONLY model transport is fake. With it, every chat and
// image request uses the actual selected Ollama adapter/provider; API, dispatch,
// Temporal, filesystem restart, constrained SVG helper and SQL are real in both.
func TestImageApplicationAPIAndRestart(t *testing.T) {
	live := os.Getenv("WEBCHAT_TEST_LIVE_IMAGES") == "1"
	var evidence *os.File
	if live {
		var e error
		path := os.Getenv("WEBCHAT_TASK9_EVIDENCE")
		require.NotEmpty(t, path)
		evidence, e = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		require.NoError(t, e)
		defer evidence.Close()
	}
	fixtures := imageFixtures(t)
	for _, model := range []string{"qwen3.5:9b", "gemma4:e4b"} {
		for _, ext := range []string{"png", "jpg", "svg"} {
			t.Run(model+"/"+ext, func(t *testing.T) {
				f := setup(t)
				root := t.TempDir()
				store, e := attachments.NewStore(root, f.repo, nil)
				require.NoError(t, e)
				defer func() { store.Close() }()
				selected := func() llm.Model {
					if !live {
						return &imageInspectModel{}
					}
					m, e := ollama.New("http://127.0.0.1:11434")
					require.NoError(t, e)
					return m
				}
				m := &tracingImageModel{base: selected()}
				startWorker := func() *worker.Runner {
					r, e := worker.StartWeb(f.ctx, f.address, &activities.ChatActivities{Repository: f.repo, Model: m, Attachments: store})
					require.NoError(t, e)
					return r
				}
				runner := startWorker()
				defer func() { runner.Close() }()
				hctx, cancel := context.WithCancel(f.ctx)
				done := make(chan struct{})
				go func() { defer close(done); f.gateway.ObserveHealth(hctx) }()
				defer func() { cancel(); <-done }()
				require.Eventually(t, func() bool { return f.gateway.Connected() && f.gateway.WorkersPolling() }, 10*time.Second, 50*time.Millisecond)
				const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
				startAPI := func() *httptest.Server {
					s := httptest.NewUnstartedServer(nil)
					h, e := api.NewServer(f.repo, api.Config{ListenAddress: s.Listener.Addr().String(), BrowserOrigin: "http://localhost:3000", ProxySecret: secret, GatewayConnected: f.gateway.Connected, WorkersPolling: f.gateway.WorkersPolling, ReconciliationReady: func() bool { return true }, Attachments: store})
					require.NoError(t, e)
					s.Config.Handler = h
					s.Start()
					return s
				}
				server := startAPI()
				defer func() { server.Close() }()
				call := func(path string, body []byte, contentType string, target any) {
					t.Helper()
					req, e := http.NewRequestWithContext(f.ctx, "POST", server.URL+path, bytes.NewReader(body))
					require.NoError(t, e)
					req.Header.Set("X-Webchat-Secret", secret)
					req.Header.Set("Content-Type", contentType)
					res, e := server.Client().Do(req)
					require.NoError(t, e)
					defer res.Body.Close()
					require.Less(t, res.StatusCode, 300, path)
					if target != nil {
						require.NoError(t, json.NewDecoder(res.Body).Decode(target))
					}
				}
				post := func(path string, body any, target any) {
					t.Helper()
					raw, e := json.Marshal(body)
					require.NoError(t, e)
					call(path, raw, "application/json", target)
				}
				var created struct{ Conversation chat.Conversation }
				post("/conversations", map[string]any{"requestId": uuid.NewString(), "model": model}, &created)
				c := created.Conversation
				t.Cleanup(func() { f.sdk.TerminateWorkflow(context.Background(), c.WorkflowID, "", "test cleanup") })
				var upload bytes.Buffer
				mw := multipart.NewWriter(&upload)
				p, e := mw.CreateFormFile("file", "bonjour."+ext)
				require.NoError(t, e)
				_, e = p.Write(fixtures[ext])
				require.NoError(t, e)
				require.NoError(t, mw.Close())
				var saved struct{ Attachment chat.Attachment }
				call("/conversations/"+string(c.ID)+"/attachments", upload.Bytes(), mw.FormDataContentType(), &saved)
				require.NotEmpty(t, saved.Attachment.ID)
				_, e = f.pool.Exec(f.ctx, "UPDATE commands SET next_attempt_at=clock_timestamp()+interval '1 day' WHERE conversation_id<>$1", c.ID)
				require.NoError(t, e)
				d, e := dispatch.New(f.repo, f.gateway, dispatch.Options{})
				require.NoError(t, e)
				dispatchOne := func() { t.Helper(); worked, e := d.RunOnce(f.ctx); require.NoError(t, e); require.True(t, worked) }
				previousHash := ""
				for round := 0; round < 2; round++ {
					instruction := "Translate the text in the image into English. Reply only with the translation."
					if round == 1 {
						instruction = "Translate the same image into English again. Reply only with the translation."
					}
					var receipt struct{ TurnID chat.TurnID }
					before := m.calls.Load()
					post("/conversations/"+string(c.ID)+"/messages", map[string]any{"requestId": uuid.NewString(), "text": instruction, "attachmentIds": []chat.AttachmentID{saved.Attachment.ID}}, &receipt)
					dispatchOne()
					approval := f.pending(t, c)
					// Workflow query state precedes its SQL projection. The API accepts
					// decisions only after this exact approval is durable (as the UI sees it).
					require.Eventually(t, func() bool {
						var durable bool
						err := f.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM approvals WHERE conversation_id=$1 AND id=$2 AND action_hash=$3 AND outcome IS NULL AND decision IS NULL)`, c.ID, approval.ID, approval.Scope.ActionHash).Scan(&durable)
						return err == nil && durable
					}, 5*time.Second, 10*time.Millisecond)
					require.Equal(t, before, m.calls.Load(), "new image action cannot invoke model before approval")
					require.NotEqual(t, previousHash, approval.Scope.ActionHash)
					previousHash = approval.Scope.ActionHash
					post("/conversations/"+string(c.ID)+"/approvals/"+approval.ID+"/decision", map[string]any{"requestId": uuid.NewString(), "actionHash": approval.Scope.ActionHash, "decision": "once"}, nil)
					dispatchOne()
					f.completed(t, chat.SessionCommand{ConversationID: c.ID, TurnID: receipt.TurnID})
					result, e := f.repo.WebToolResult(f.ctx, c.ID, approval.Scope.ToolCallID)
					require.NoError(t, e)
					require.NotNil(t, result.Analysis)
					require.Equal(t, chat.NarrowImageProfile, result.Analysis.ImageProfile)
					require.NotEmpty(t, result.Text)
					require.EqualValues(t, round+1, m.images.Load(), "one actual provider image request per separately approved action")
					in := chat.DocumentAnalysisInput{Scope: approval.Scope, Model: model, Budget: chat.DefaultDocumentBudget()}
					history := f.sdk.GetWorkflowHistory(f.ctx, in.ChildID(), "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
					invoked := false
					for history.HasNext() {
						event, e := history.Next()
						require.NoError(t, e)
						if a := event.GetActivityTaskScheduledEventAttributes(); a != nil && a.ActivityType.Name == "AnalyzeAttachmentImage" {
							invoked = true
						}
						raw, e := json.Marshal(event)
						require.NoError(t, e)
						require.NotContains(t, string(raw), "iVBOR")
						require.NotContains(t, string(raw), result.Text)
					}
					require.True(t, invoked)
					var missing int
					require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM web_tool_invocations WHERE turn_id=$1 AND cleanup_confirmed_at IS NULL", receipt.TurnID).Scan(&missing))
					require.Zero(t, missing)
					semantic := strings.Contains(strings.ToLower(result.Text), "hello") && strings.Contains(strings.ToLower(result.Text), "world")
					t.Logf("live=%t model=%s fixture=%s round=%d semanticAccepted=%t durableImageResult=%q", live, model, ext, round, semantic, result.Text)
					if evidence != nil {
						require.NoError(t, json.NewEncoder(evidence).Encode(map[string]any{"model": model, "fixture": ext, "restartFollowUp": round == 1, "profile": chat.NarrowImageProfile, "imageAdmissionQualified": false, "semanticAccepted": semantic, "result": result, "childId": in.ChildID()}))
						require.NoError(t, evidence.Sync())
					}
					if round == 0 {
						server.Close()
						require.NoError(t, runner.Close())
						require.NoError(t, store.Close())
						store, e = attachments.NewStore(root, f.repo, nil)
						require.NoError(t, e)
						require.NoError(t, store.Recover(f.ctx))
						m.base = selected()
						runner = startWorker()
						server = startAPI()
						original, e := store.Open(f.ctx, c.ID, saved.Attachment.ID)
						require.NoError(t, e)
						var b bytes.Buffer
						_, e = b.ReadFrom(original)
						require.NoError(t, e)
						require.NoError(t, original.Close())
						require.Equal(t, fixtures[ext], b.Bytes(), "original survives actual store close/reopen")
					}
				}
			})
		}
	}
}
