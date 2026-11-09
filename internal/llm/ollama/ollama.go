// Package ollama adapts the pinned Ollama streaming chat API to the model boundary.
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/ollama/ollama/api"
)

// ModelNotFoundError identifies an unavailable model (HTTP 404).
type ModelNotFoundError struct{ Err error }

func (e *ModelNotFoundError) Error() string { return "ollama model not found: " + e.Err.Error() }
func (e *ModelNotFoundError) Unwrap() error { return e.Err }

// InvalidRequestError identifies invalid local input or HTTP 400/422 responses.
type InvalidRequestError struct{ Err error }

func (e *InvalidRequestError) Error() string { return "invalid ollama request: " + e.Err.Error() }
func (e *InvalidRequestError) Unwrap() error { return e.Err }

type Model struct{ client *api.Client }

// New requires an explicit HTTP(S) base URL; it does not read cloud credentials
// or select a provider from the environment.
func New(host string) (*Model, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid Ollama host %q: expected HTTP(S) URL", host)
	}
	return &Model{client: api.NewClient(u, http.DefaultClient)}, nil
}

func (m *Model) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	stream := true
	wire := &api.ChatRequest{Model: req.Model, Stream: &stream}
	for _, msg := range req.Messages {
		out := api.Message{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID, ToolName: msg.ToolName}
		for _, call := range msg.ToolCalls {
			var args api.ToolCallFunctionArguments
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return llm.Response{}, &InvalidRequestError{err}
			}
			out.ToolCalls = append(out.ToolCalls, api.ToolCall{ID: call.ID, Function: api.ToolCallFunction{Name: call.Name, Arguments: args}})
		}
		wire.Messages = append(wire.Messages, out)
	}
	for _, tool := range req.Tools {
		var params api.ToolFunctionParameters
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			return llm.Response{}, &InvalidRequestError{err}
		}
		wire.Tools = append(wire.Tools, api.Tool{Type: "function", Function: api.ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: params}})
	}
	res := llm.Response{Message: llm.Message{Role: "assistant"}}
	var callbackErr error
	err := m.client.Chat(ctx, wire, func(chunk api.ChatResponse) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		res.Message.Content += chunk.Message.Content
		for _, call := range chunk.Message.ToolCalls {
			args, err := json.Marshal(call.Function.Arguments)
			if err != nil {
				return err
			}
			id := call.ID
			if id == "" {
				id = fmt.Sprintf("ollama-%d-%d", req.Turn, len(res.Message.ToolCalls))
			}
			res.Message.ToolCalls = append(res.Message.ToolCalls, llm.ToolCall{ID: id, Name: call.Function.Name, Args: args})
		}
		// EvalCount is cumulative; do not count text chunks as tokens.
		delta := chunk.EvalCount - res.Tokens
		if delta < 0 {
			delta = 0
		}
		res.Tokens += delta
		if emit != nil && (chunk.Message.Content != "" || delta != 0) {
			callbackErr = emit(llm.Delta{Text: chunk.Message.Content, Tokens: delta})
			if callbackErr != nil {
				return callbackErr
			}
		}
		return ctx.Err()
	})
	if callbackErr != nil {
		return llm.Response{}, callbackErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		var status api.StatusError
		if errors.As(err, &status) {
			switch status.StatusCode {
			case 404:
				err = &ModelNotFoundError{err}
			case 400, 422:
				err = &InvalidRequestError{err}
			}
		}
		return llm.Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return res, nil
}

var _ llm.Model = (*Model)(nil)
