package ollama

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/model/renderers"
	"golang.org/x/text/unicode/norm"
)

// textWire is the shared text conversion for dispatch and rendered admission.
// Nonpositive options and nil thinking preserve legacy provider defaults.
// Image envelopes and bounded dispatch enforcement remain separate.
func textWire(req llm.Request) (*api.ChatRequest, error) {
	stream := true
	wire := &api.ChatRequest{Model: req.Model, Stream: &stream}
	if req.ContextWindow > 0 || req.MaxOutputTokens > 0 {
		wire.Options = map[string]any{}
		if req.ContextWindow > 0 {
			wire.Options["num_ctx"] = req.ContextWindow
		}
		if req.MaxOutputTokens > 0 {
			wire.Options["num_predict"] = req.MaxOutputTokens
		}
	}
	if req.Think != nil {
		wire.Think = &api.ThinkValue{Value: *req.Think}
	}
	for _, msg := range req.Messages {
		out := api.Message{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID, ToolName: msg.ToolName}
		for _, call := range msg.ToolCalls {
			var args api.ToolCallFunctionArguments
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return nil, &InvalidRequestError{err}
			}
			out.ToolCalls = append(out.ToolCalls, api.ToolCall{ID: call.ID, Function: api.ToolCallFunction{Name: call.Name, Arguments: args}})
		}
		wire.Messages = append(wire.Messages, out)
	}
	for _, tool := range req.Tools {
		var params api.ToolFunctionParameters
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			return nil, &InvalidRequestError{err}
		}
		wire.Tools = append(wire.Tools, api.Tool{Type: "function", Function: api.ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: params}})
	}
	return wire, nil
}

// TextInputBound is a conservative source-derived upper bound, NOT measured
// tokens. Count the actual pinned renderer (including schemas/tool scaffolding)
// under both possible thinking defaults. Qwen NFC can EXPAND Unicode: summing
// per-rune NFD bytes bounds every normalized segment even across added-token
// splits. Gemma's space replacement expands to three bytes. Neither pinned
// tokenizer inserts pretokenizer prefixes. BPE/byte fallback cannot exceed the
// resulting bytes. 512 covers BOS/framing beyond the renderer. Keep the older
// JSON-byte estimate as an additional constraint, not a tokenizer claim.
func TextInputBound(req llm.Request) (int, error) {
	_, renderer, _ := imageIdentity(req.Model)
	if renderer == "" || req.Image != nil {
		return 0, &InvalidRequestError{fmt.Errorf("unsupported bounded text model")}
	}
	wire, err := textWire(req)
	if err != nil {
		return 0, err
	}
	cost := 0
	for _, m := range req.Messages {
		if len(m.Images) > 0 || !utf8.ValidString(m.Content) || strings.Contains(m.Content, "[img-") {
			return 0, &InvalidRequestError{fmt.Errorf("invalid bounded text")}
		}
		raw, e := json.Marshal(m)
		if e != nil {
			return 0, &InvalidRequestError{e}
		}
		cost += len(raw) + 128
	}
	raw, err := json.Marshal(req.Tools)
	if err != nil {
		return 0, &InvalidRequestError{err}
	}
	cost += len(raw)
	for _, think := range []bool{false, true} {
		rendered, e := renderers.RenderWithRenderer(renderer, wire.Messages, wire.Tools, &api.ThinkValue{Value: think})
		if e != nil {
			return 0, &InvalidRequestError{e}
		}
		n := 512
		for _, r := range rendered {
			if req.Model == "qwen3.5:9b" {
				n += len(norm.NFD.String(string(r)))
			} else if r == ' ' {
				n += 3
			} else {
				n += utf8.RuneLen(r)
			}
		}
		if n > cost {
			cost = n
		}
	}
	return cost, nil
}

// ImageInputBound delegates admission to the unchanged Task9 fixed envelope.
// It is never used to authorize arbitrary image/history combinations.
func ImageInputBound(req llm.Request) (text, images int, err error) {
	wire, err := imageEnvelope(req)
	if err != nil {
		return 0, 0, err
	}
	_, renderer, images := imageIdentity(req.Model)
	rendered, err := renderers.RenderWithRenderer(renderer, wire.Messages, nil, wire.Think)
	return 3*len(rendered) + 512, images, err
}
