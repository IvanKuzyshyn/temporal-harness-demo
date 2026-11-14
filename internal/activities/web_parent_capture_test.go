package activities

// Explicit, opt-in, test-only parent observation. Never starts workflows, reads
// attachments or executes returned tools. Default tests cannot reach a provider.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	"github.com/ollama/ollama/api"
	"github.com/stretchr/testify/require"
)

type parentRequestRecorder struct {
	llm.Model
	Request llm.Request
}

func (r *parentRequestRecorder) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	r.Request = req
	return r.Model.Chat(ctx, req, emit)
}
func captureWrite(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
func captureJSON(path string, data any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return captureWrite(path, b)
}
func captureHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

type parentCaptureTransport struct {
	base                  http.RoundTripper
	origin, dir, metadata string
	planned               []byte
	prepare               bool
}

func (c *parentCaptureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != c.origin {
		return nil, fmt.Errorf("unapproved capture origin")
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if r.Method == "GET" && (name == "version" || name == "tags") {
		if c.prepare {
			b, e := os.ReadFile(filepath.Join(c.metadata, name+".json"))
			if e != nil {
				return nil, e
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
		}
		res, e := c.base.RoundTrip(r)
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if e != nil {
			return nil, e
		}
		if e = captureWrite(filepath.Join(c.dir, "observed-"+name+".json"), b); e != nil {
			return nil, e
		}
		res.Body = io.NopCloser(bytes.NewReader(b))
		return res, nil
	}
	if r.Method != "POST" || name != "chat" {
		return nil, fmt.Errorf("unapproved capture endpoint")
	}
	b, e := io.ReadAll(r.Body)
	r.Body.Close()
	if e != nil {
		return nil, e
	}
	if c.prepare {
		if e = captureWrite(filepath.Join(c.dir, "intended-request.json"), b); e != nil {
			return nil, e
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{\"done\":true,\"message\":{\"role\":\"assistant\",\"content\":\"SYNTHETIC PREPARATION ONLY\"}}\n")), Request: r}, nil
	}
	if !bytes.Equal(b, c.planned) {
		return nil, fmt.Errorf("request differs from frozen predeclared bytes")
	}
	// Consumed BEFORE attempting network I/O. An error/timeout/empty body never
	// frees the budget. O_EXCL also fences process reruns against this same ledger.
	started := time.Now().UTC()
	if e = captureJSON(filepath.Join(c.dir, "invocation.json"), map[string]any{"started": started, "requestSHA256": captureHash(b), "url": r.URL.String(), "maximumPhysicalCalls": 1}); e != nil {
		return nil, e
	}
	if e = captureWrite(filepath.Join(c.dir, "outgoing-request.json"), b); e != nil {
		return nil, e
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.GetBody = nil
	res, networkErr := c.base.RoundTrip(r)
	headersAt := time.Now().UTC()
	var raw []byte
	var readErr error
	if networkErr == nil {
		// Buffer provider NDJSON before the unchanged adapter parses it, so even an
		// adapter rejection retains the stream. Eight MiB is an artifact safety bound,
		// not a changed model/output cap. Exceeding it aborts, never retries.
		raw, readErr = io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
		res.Body.Close()
		if e = captureWrite(filepath.Join(c.dir, "provider.ndjson"), raw); e != nil {
			return nil, e
		}
		if len(raw) > 8<<20 {
			readErr = fmt.Errorf("capture artifact exceeded 8 MiB; stream incomplete")
		}
	}
	record := map[string]any{"started": started, "headersAt": headersAt, "finished": time.Now().UTC(), "durationMS": time.Since(started).Milliseconds(), "networkError": fmt.Sprint(networkErr), "readError": fmt.Sprint(readErr), "rawBytes": len(raw), "rawSHA256": captureHash(raw), "completeHTTPBody": networkErr == nil && readErr == nil}
	if res != nil {
		record["status"] = res.StatusCode
		record["headers"] = res.Header
	}
	if e = captureJSON(filepath.Join(c.dir, "http-result.json"), record); e != nil {
		return nil, e
	}
	if networkErr != nil {
		return nil, networkErr
	}
	if readErr != nil {
		return nil, readErr
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return res, nil
}

// Fixture-only guard proof, including a failure consuming its budget. No socket
// is opened by this test. Preparation and capture are separate explicit modes.
type captureRoundTripFunc func(*http.Request) (*http.Response, error)

func (f captureRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestParentCaptureGuard(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			calls := 0
			c := &parentCaptureTransport{origin: "http://fixture.invalid", dir: t.TempDir(), planned: []byte("frozen"), base: captureRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if fail {
					return nil, fmt.Errorf("synthetic network failure")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{\"done\":true}\n"))}, nil
			})}
			invoke := func(body string) error {
				r, e := http.NewRequest("POST", "http://fixture.invalid/api/chat", strings.NewReader(body))
				require.NoError(t, e)
				_, e = c.RoundTrip(r)
				return e
			}
			require.Error(t, invoke("changed"))
			require.Zero(t, calls)
			err := invoke("frozen")
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls)
			// A new wrapper instance still cannot reuse an already consumed directory.
			clone := *c
			c = &clone
			require.Error(t, invoke("frozen"))
			require.Equal(t, 1, calls)
		})
	}
}

func TestParentBoundaryCapture(t *testing.T) {
	root := os.Getenv("WEBCHAT_R2_CAPTURE_DIR")
	mode := os.Getenv("WEBCHAT_R2_CAPTURE_MODE")
	if root == "" || mode == "" {
		t.Skip("explicit test-only capture opt-in required")
	}
	require.Contains(t, []string{"prepare", "once"}, mode)
	var artifact struct{ Request llm.Request }
	b, err := os.ReadFile(filepath.Join(root, "reconstruction", "parent-reconstruction.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &artifact))
	models := []string{"qwen3.5:9b", "gemma4:e4b"}
	if mode == "once" {
		model := os.Getenv("WEBCHAT_R2_CAPTURE_MODEL")
		require.Contains(t, models, model)
		models = []string{model} // exactly one explicit physical-call target per process
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			req := artifact.Request
			req.Model = model
			require.True(t, req.Bounded)
			require.Nil(t, req.Think)
			require.Nil(t, req.Image)
			require.Equal(t, 16384, req.ContextWindow)
			require.Equal(t, 2000, req.MaxOutputTokens)
			require.Equal(t, 65536, req.MaxResponseBytes)
			require.Len(t, req.Messages, 4)
			require.Len(t, req.Tools, 2)
			for _, m := range req.Messages {
				require.Empty(t, m.Images)
			}
			cost, err := ollama.TextInputBound(req)
			require.NoError(t, err)
			require.LessOrEqual(t, cost+2000+2384, 16384)
			dir := filepath.Join(root, strings.ReplaceAll(model, ":", "-"))
			if mode == "prepare" {
				require.NoError(t, os.Mkdir(dir, 0700))
			}
			transport := &http.Transport{DisableKeepAlives: true} // fresh connections: no idle-connection replay
			defer transport.CloseIdleConnections()
			wrapper := &parentCaptureTransport{base: transport, origin: "http://127.0.0.1:11434", dir: dir, metadata: filepath.Join(root, "metadata"), prepare: mode == "prepare"}
			if mode == "once" {
				wrapper.planned, err = os.ReadFile(filepath.Join(dir, "intended-request.json"))
				require.NoError(t, err)
				approved, e := os.ReadFile(filepath.Join(dir, "predeclared.sha256"))
				require.NoError(t, e)
				require.Equal(t, captureHash(wrapper.planned), strings.TrimSpace(string(approved)))
			} else {
				require.NoError(t, captureJSON(filepath.Join(dir, "adapter-request.json"), map[string]any{"request": req, "inputBound": cost, "kind": "reconstructed Qwen post-tool input; Gemma differs only by model"}))
			}
			old := http.DefaultClient
			http.DefaultClient = &http.Client{Transport: wrapper, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			defer func() { http.DefaultClient = old }()
			adapter, err := ollama.New(wrapper.origin)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			var deltas []llm.Delta
			result, callErr := adapter.Chat(ctx, req, func(d llm.Delta) error { deltas = append(deltas, d); return nil })
			if mode == "prepare" {
				require.NoError(t, callErr)
				return
			}
			require.NoError(t, captureJSON(filepath.Join(dir, "adapter-result.json"), map[string]any{"response": result, "error": fmt.Sprint(callErr), "deltas": deltas, "returnedToolsRefused": len(result.Message.ToolCalls) > 0, "toolsExecuted": 0}))
			raw, rawErr := os.ReadFile(filepath.Join(dir, "provider.ndjson"))
			if rawErr == nil {
				var chunks []api.ChatResponse
				var thinking, content strings.Builder
				var calls []api.ToolCall
				var decodeErrors []string
				for i, line := range bytes.Split(raw, []byte("\n")) {
					if len(bytes.TrimSpace(line)) == 0 {
						continue
					}
					var chunk api.ChatResponse
					if e := json.Unmarshal(line, &chunk); e != nil {
						decodeErrors = append(decodeErrors, fmt.Sprintf("line %d: %v", i+1, e))
						continue
					}
					chunks = append(chunks, chunk)
					thinking.WriteString(chunk.Message.Thinking)
					content.WriteString(chunk.Message.Content)
					calls = append(calls, chunk.Message.ToolCalls...)
				}
				require.NoError(t, captureJSON(filepath.Join(dir, "decoded.json"), map[string]any{"chunks": chunks, "decodeErrors": decodeErrors, "toolCalls": calls}))
				require.NoError(t, captureWrite(filepath.Join(dir, "thinking.txt"), []byte(thinking.String())))
				require.NoError(t, captureWrite(filepath.Join(dir, "content.txt"), []byte(content.String())))
				if callErr == nil {
					require.Equal(t, content.String(), result.Message.Content)
				}
			}
			// Returned tools are inert observations only. No workflow or tool runner is
			// present in this harness. Semantic assertion is run unchanged by Node later.
			require.NoError(t, callErr, "capture failure consumes budget; NEVER retry")
			require.Empty(t, result.Message.ToolCalls, "returned tool request refused, NEVER executed")
		})
	}
}
