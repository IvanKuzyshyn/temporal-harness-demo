package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/stretchr/testify/require"
)

// Catch dropped/changed wire fields and admission/dispatch drift, especially
// accidentally overriding legacy provider defaults with zero options.
func TestTextWireAdmissionDispatchParity(t *testing.T) {
	off, on := false, true
	for _, model := range []string{"qwen3.5:9b"} {
		for _, tc := range []struct {
			name            string
			context, output int
			think           *bool
			bounded         bool
			options         string
		}{
			{"legacy_default", 0, 0, nil, false, `null`},
			{"legacy_nonpositive", -1, -1, nil, false, `null`},
			{"context_only", 16384, 0, &off, false, `{"num_ctx":16384}`},
			{"output_only", 0, 2000, &on, false, `{"num_predict":2000}`},
			{"positive_explicit_thinking", 16384, 2000, &on, false, `{"num_ctx":16384,"num_predict":2000}`},
			{"bounded_default_thinking", 16384, 2000, nil, true, `{"num_ctx":16384,"num_predict":2000}`},
			{"bounded_no_thinking", 16384, 2000, &off, true, `{"num_ctx":16384,"num_predict":2000}`},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				req := llm.Request{Model: model, ContextWindow: tc.context, MaxOutputTokens: tc.output, Think: tc.think, Bounded: tc.bounded,
					Messages: []llm.Message{
						{Role: "system", Content: "System instruction."},
						{Role: "assistant", Content: "Calling tools.", ToolCalls: []llm.ToolCall{
							{ID: "call-1", Name: "inspect", Args: json.RawMessage(`{"limits":{"names":["α","b"],"enabled":true},"n":2,"none":null}`)},
							{ID: "call-2", Name: "ping", Args: json.RawMessage(`{}`)},
						}},
						{Role: "tool", ToolCallID: "call-1", ToolName: "inspect", Content: "Recorded result."},
						{Role: "tool", ToolCallID: "call-2", ToolName: "ping", Content: "pong"},
						{Role: "user", Content: "Current question."},
					},
					Tools: []llm.ToolDefinition{
						{Name: "inspect", Description: "Nested schema.", Parameters: json.RawMessage(`{"type":"object","properties":{"limits":{"type":"object","properties":{"names":{"type":"array","items":{"type":"string"}},"enabled":{"type":"boolean"}},"required":["names"]},"n":{"type":["number","null"]}},"required":["limits"]}`)},
						{Name: "ping", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
					},
				}
				// Independent literal expectations protect parity from a common converter
				// dropping fields on BOTH paths. Preserve nested fields and list order.
				var want map[string]any
				require.NoError(t, json.Unmarshal([]byte(`{"model":"","stream":true,"options":null,"messages":[
     {"role":"system","content":"System instruction."},
     {"role":"assistant","content":"Calling tools.","tool_calls":[
      {"id":"call-1","function":{"index":0,"name":"inspect","arguments":{"limits":{"names":["α","b"],"enabled":true},"n":2,"none":null}}},
      {"id":"call-2","function":{"index":0,"name":"ping","arguments":{}}}]},
     {"role":"tool","content":"Recorded result.","tool_call_id":"call-1","tool_name":"inspect"},
     {"role":"tool","content":"pong","tool_call_id":"call-2","tool_name":"ping"},
     {"role":"user","content":"Current question."}],
     "tools":[{"type":"function","function":{"name":"inspect","description":"Nested schema.","parameters":{"type":"object","properties":{"limits":{"type":"object","properties":{"names":{"type":"array","items":{"type":"string"}},"enabled":{"type":"boolean"}},"required":["names"]},"n":{"type":["number","null"]}},"required":["limits"]}}},
     {"type":"function","function":{"name":"ping","parameters":{"type":"object","properties":{}}}}]}`), &want))
				want["model"] = model
				var options any
				require.NoError(t, json.Unmarshal([]byte(tc.options), &options))
				want["options"] = options
				if tc.think != nil {
					want["think"] = *tc.think
				}

				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/version":
						io.WriteString(w, `{"version":"0.40.15"}`)
					case "/api/tags":
						io.WriteString(w, `{"models":[{"name":"qwen3.5:9b","digest":"d63f830f28d515fefea546f213fca7a75e9dd5176ef51a6deabc952d1c99a911","details":{"format":"safetensors","runner":"mlx"}},{"name":"gemma4:e4b","digest":"eb2177adf7646d15faead94fdad6d3e170dc8015653418a3b7d5d52a6380be5b","details":{"format":"safetensors","runner":"mlx"}}]}`)
					case "/api/chat":
						raw, e := io.ReadAll(r.Body)
						if e != nil {
							t.Error(e)
							http.Error(w, "read", 500)
							return
						}
						captured <- raw
						io.WriteString(w, "{\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"done\":true}\n")
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				m, e := New(server.URL)
				require.NoError(t, e)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, e = m.Chat(ctx, req, nil)
				require.NoError(t, e)
				raw := <-captured
				var dispatched map[string]any
				require.NoError(t, json.Unmarshal(raw, &dispatched))
				if tc.bounded {
					require.Equal(t, false, dispatched["truncate"])
					require.Equal(t, false, dispatched["shift"])
					require.Equal(t, "mlx", dispatched["runner"])
					delete(dispatched, "truncate")
					delete(dispatched, "shift")
					delete(dispatched, "runner")
				}
				require.Equal(t, want, dispatched, "preserve actual legacy dispatch and explicit positive options")
				wire, e := textWire(req)
				require.NoError(t, e)
				converted, e := json.Marshal(wire)
				require.NoError(t, e)
				var admitted map[string]any
				require.NoError(t, json.Unmarshal(converted, &admitted))
				require.Equal(t, want, admitted, "admission conversion must match dispatch, including unset options")
				// Insertion order also affects rendering; don't hide it by decoding to maps.
				var sentFields, convertedFields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &sentFields))
				require.NoError(t, json.Unmarshal(converted, &convertedFields))
				require.Equal(t, string(sentFields["messages"]), string(convertedFields["messages"]))
				require.Equal(t, string(sentFields["tools"]), string(convertedFields["tools"]))
				cost, e := TextInputBound(req)
				require.NoError(t, e)
				require.Positive(t, cost)
			})
		}
	}
}

// Both consumers must preserve typed decode errors and must not send malformed
// arguments/schemas (including identity probes) to the provider.
func TestTextWireRejectsMalformedBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, args, params string
		syntax             bool
	}{
		{"arguments_syntax", `{"nested":`, `{"type":"object","properties":{}}`, true},
		{"arguments_not_object", `[]`, `{"type":"object","properties":{}}`, false},
		{"schema_syntax", `{}`, `{"properties":`, true},
		{"schema_wrong_type", `{}`, `{"type":42}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := llm.Request{Bounded: true, Model: "qwen3.5:9b", ContextWindow: 16384, MaxOutputTokens: 2000,
				Messages: []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "bad", Name: "inspect", Args: json.RawMessage(tc.args)}}}},
				Tools:    []llm.ToolDefinition{{Name: "inspect", Parameters: json.RawMessage(tc.params)}},
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected HTTP", 500) }))
			defer server.Close()
			m, e := New(server.URL)
			require.NoError(t, e)
			wire, conversionErr := textWire(req)
			require.Nil(t, wire)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, dispatchErr := m.Chat(ctx, req, nil)
			require.Equal(t, llm.Response{}, result)
			cost, boundErr := TextInputBound(req)
			require.Zero(t, cost)
			for _, err := range []error{conversionErr, dispatchErr, boundErr} {
				var invalid *InvalidRequestError
				require.ErrorAs(t, err, &invalid)
				require.NotNil(t, invalid.Unwrap())
				if tc.syntax {
					var syntax *json.SyntaxError
					require.ErrorAs(t, err, &syntax)
				}
			}
			require.EqualError(t, dispatchErr, conversionErr.Error())
			require.EqualError(t, boundErr, conversionErr.Error())
			require.Zero(t, calls.Load())
		})
	}
}
