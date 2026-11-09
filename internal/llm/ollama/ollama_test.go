package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/ollama/ollama/api"
)

func TestWire(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/api/chat" || body["stream"] != true || body["model"] != "local" {
			t.Errorf("request: %v", body)
		}
		tools := body["tools"].([]any)
		f := tools[0].(map[string]any)["function"].(map[string]any)
		if f["name"] != "read_file" || f["parameters"].(map[string]any)["required"].([]any)[0] != "path" {
			t.Errorf("tool: %v", f)
		}
		msgs := body["messages"].([]any)
		if msgs[1].(map[string]any)["tool_call_id"] != "prior" {
			t.Errorf("messages: %v", msgs)
		}
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hello "}}`)
		fmt.Fprintln(w, `{"message":{"content":"world","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"calc/calc.go"}}}]}}`)
		fmt.Fprintln(w, `{"message":{},"done":true,"eval_count":7}`)
	}))
	defer s.Close()
	m, err := New(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Model: "local", Turn: 2, Tools: []llm.ToolDefinition{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}, Messages: []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "prior", Name: "read_file", Args: json.RawMessage(`{"path":"."}`)}}}, {Role: "tool", ToolCallID: "prior", ToolName: "read_file", Content: "ok"}}}
	var text string
	var tokens int
	res, err := m.Chat(context.Background(), req, func(d llm.Delta) error { text += d.Text; tokens += d.Tokens; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello world" || res.Message.Content != text || tokens != 7 || res.Tokens != 7 || len(res.Message.ToolCalls) != 1 || res.Message.ToolCalls[0].ID != "ollama-2-0" {
		t.Fatalf("%+v text=%q tokens=%d", res, text, tokens)
	}
}

func TestErrors(t *testing.T) {
	for _, code := range []int{400, 404, 422, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprintln(w, `{"error":"bad model"}`)
			}))
			defer s.Close()
			m, _ := New(s.URL)
			_, err := m.Chat(context.Background(), llm.Request{}, nil)
			var missing *ModelNotFoundError
			var invalid *InvalidRequestError
			if err == nil || errors.As(err, &missing) != (code == 404) || errors.As(err, &invalid) != (code == 400 || code == 422) {
				t.Fatalf("%T %v", err, err)
			}
		})
	}
}
func TestStreamFailures(t *testing.T) {
	sentinel := errors.New("callback")
	for _, mode := range []string{"callback", "cancel", "stream", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "stream" {
					fmt.Fprintln(w, `{"error":"generation failed"}`)
					return
				}
				if mode == "malformed" {
					fmt.Fprintln(w, `not json`)
					return
				}
				fmt.Fprintln(w, `{"message":{"content":"hi"}}`)
				w.(http.Flusher).Flush()
				if mode == "cancel" {
					<-r.Context().Done()
				}
			}))
			defer s.Close()
			m, _ := New(s.URL)
			_, err := m.Chat(ctx, llm.Request{}, func(llm.Delta) error {
				if mode == "cancel" {
					cancel()
					return nil
				}
				return sentinel
			})
			if err == nil || (mode == "callback" && !errors.Is(err, sentinel)) || (mode == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("%v", err)
			}
		})
	}
}
func TestIncompleteStreams(t *testing.T) {
	for _, body := range []string{"", `{"message":{"content":"partial","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"calc/calc.go"}}}]}}` + "\n"} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer s.Close()
			m, _ := New(s.URL)
			res, err := m.Chat(context.Background(), llm.Request{}, nil)
			if !errors.Is(err, io.ErrUnexpectedEOF) || !reflect.DeepEqual(res, llm.Response{}) {
				t.Fatalf("response=%+v error=%v", res, err)
			}
		})
	}
}

func TestEmptyHTTPFailures(t *testing.T) {
	for _, code := range []int{400, 404, 422, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer s.Close()
			m, _ := New(s.URL)
			res, err := m.Chat(context.Background(), llm.Request{}, nil)
			var status api.StatusError
			var missing *ModelNotFoundError
			var invalid *InvalidRequestError
			if !errors.As(err, &status) || status.StatusCode != code ||
				errors.As(err, &missing) != (code == 404) ||
				errors.As(err, &invalid) != (code == 400 || code == 422) ||
				!reflect.DeepEqual(res, llm.Response{}) {
				t.Fatalf("response=%+v error=%T %v", res, err, err)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	for _, host := range []string{"", ":bad", "ftp://localhost", "http://"} {
		if _, err := New(host); err == nil {
			t.Errorf("accepted %q", host)
		}
	}
	m, _ := New("http://localhost:1")
	_, err := m.Chat(context.Background(), llm.Request{Tools: []llm.ToolDefinition{{Parameters: json.RawMessage(`no`)}}}, nil)
	var invalid *InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.Chat(ctx, llm.Request{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
