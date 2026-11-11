package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests catch a runner that accepts buffered-only responses, incomplete
// streams, fallback routing, invented tools, incorrect translations or HTTP errors.
func TestChatQualification(t *testing.T) {
	for _, tc := range []struct {
		name, kind, fault string
		fail              bool
	}{
		{"streamed text", "text", "", false},
		{"buffered text is not streaming", "text", "buffered", true},
		{"missing done", "text", "incomplete", true},
		{"wrong model", "text", "fallback", true},
		{"provider failure", "text", "http", true},
		{"malformed stream", "text", "malformed", true},
		{"tool arguments", "tools", "", false},
		{"wrong tool", "tools", "wrong-tool", true},
		{"wrong URL type", "tools", "bad-args", true},
		{"extra tool property", "tools", "extra-args", true},
		{"translated image", "image", "", false},
		{"incorrect translation", "image", "bad-translation", true},
		{"long context", "context", "", false},
		{"lost early marker", "context", "lost-marker", true},
		{"silent context truncation", "context", "truncated-context", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req chatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/api/chat" || req.Model != "qwen3.5:9b" || !req.Stream || req.Options.NumCtx != 16384 || req.Options.NumPredict != 2000 {
					t.Errorf("incorrect request: %+v", req)
				}
				if tc.kind == "image" && len(req.Messages[0].Images) != 1 {
					t.Error("image bytes omitted")
				}
				if tc.kind == "tools" && len(req.Tools) != 1 {
					t.Error("tool schema omitted")
				}
				if tc.fault == "http" {
					http.Error(w, "unavailable", 503)
					return
				}
				if tc.fault == "malformed" {
					fmt.Fprintln(w, "not json")
					return
				}
				model := "qwen3.5:9b"
				if tc.fault == "fallback" {
					model = "gemma4:e4b"
				}
				content := []string{"Hello", " world"}
				if tc.kind == "context" {
					if !strings.Contains(req.Messages[0].Content, "orchid-731") || len(req.Messages[0].Content) < 22000 {
						t.Error("long-context probe omitted early marker or filler")
					}
					content = []string{"orchid", "-731"}
					if tc.fault == "lost-marker" {
						content = []string{"unknown"}
					}
				}
				if tc.kind == "image" && tc.fault == "bad-translation" {
					content = []string{"Goodbye", " moon"}
				}
				if tc.fault == "buffered" {
					content = nil
				}
				for _, text := range content {
					json.NewEncoder(w).Encode(map[string]any{"model": model, "message": map[string]any{"role": "assistant", "content": text}, "done": false})
					w.(http.Flusher).Flush()
				}
				if tc.fault == "incomplete" {
					return
				}
				msg := map[string]any{"role": "assistant", "content": ""}
				if tc.fault == "buffered" {
					msg["content"] = "Hello world"
				}
				if tc.kind == "tools" {
					name := "web_fetch"
					args := map[string]any{"url": "https://example.com/"}
					if tc.fault == "wrong-tool" {
						name = "exec"
					}
					if tc.fault == "bad-args" {
						args["url"] = 42
					}
					if tc.fault == "extra-args" {
						args["command"] = "whoami"
					}
					msg["tool_calls"] = []any{map[string]any{"function": map[string]any{"name": name, "arguments": args}}}
				}
				promptTokens := 100
				if tc.kind == "context" && tc.fault != "truncated-context" {
					promptTokens = 11042
				}
				json.NewEncoder(w).Encode(map[string]any{"model": model, "message": msg, "done": true, "prompt_eval_count": promptTokens, "eval_count": 2})
			}))
			defer server.Close()
			result := qualifyChat(context.Background(), server.Client(), server.URL, "qwen3.5:9b", tc.kind, []byte("fixture"))
			if (result.Error != "") != tc.fail {
				t.Fatalf("want failure=%v, got %+v", tc.fail, result)
			}
		})
	}
}

func TestCancellationClosesActiveStream(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"model":"gemma4:e4b","message":{"content":"one"},"done":false}`)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()
	result := qualifyChat(context.Background(), server.Client(), server.URL, "gemma4:e4b", "cancel", nil)
	if result.Error != "" || !result.Cancelled {
		t.Fatalf("cancellation not observed: %+v", result)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("provider request still running")
	}
}

func TestCancellationCannotPassCompletedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"model":"gemma4:e4b","message":{"content":"done"},"done":true}`)
	}))
	defer server.Close()
	if result := qualifyChat(context.Background(), server.Client(), server.URL, "gemma4:e4b", "cancel", nil); result.Error == "" {
		t.Fatal("completed generation was certified as cancelled")
	}
}

func TestUnavailableModelRequiresExplicitNotFound(t *testing.T) {
	for _, code := range []int{200, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, `{"error":"model 'webchat-qualification-missing:never' not found"}`)
			}))
			defer server.Close()
			result := qualifyUnavailable(context.Background(), server.Client(), server.URL)
			if (result.Error == "") != (code == 404) {
				t.Fatalf("HTTP %d: %+v", code, result)
			}
		})
	}
}

func TestContextConfigurationRequiresActiveExact16K(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fail       bool
	}{
		{"matching", `{"models":[{"name":"qwen3.5:9b","context_length":16384}]}`, false},
		{"too small", `{"models":[{"name":"qwen3.5:9b","context_length":4096}]}`, true},
		{"wrong model", `{"models":[{"name":"gemma4:e4b","context_length":16384}]}`, true},
		{"not running", `{"models":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/ps" {
					t.Errorf("wrong metadata route: %s", r.URL.Path)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := contextSize(context.Background(), server.Client(), server.URL, "qwen3.5:9b")
			if (err != nil) != tc.fail {
				t.Fatalf("want failure=%v, got %v", tc.fail, err)
			}
		})
	}
}

func TestUnknownModelRejectedBeforeHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unknown model reached provider") }))
	defer server.Close()
	result := qualifyChat(context.Background(), server.Client(), server.URL, "other:latest", "text", nil)
	if !strings.Contains(result.Error, "model") {
		t.Fatalf("missing routing rejection: %+v", result)
	}
}
