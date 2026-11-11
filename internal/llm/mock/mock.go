// Package mock supplies deterministic local model responses without network access.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

// ErrModel is the deterministic failure returned by the error scenario.
var ErrModel = errors.New("mock model error")

type Model struct{}

func New() *Model { return &Model{} }

// Chat uses Turn (zero based) or completed assistant/tool transcript rounds,
// whichever is greater. No state is retained between requests. Approval is the
// harness's responsibility; the mock only proposes tool calls.
func (*Model) Chat(ctx context.Context, req llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	scenario := strings.ToLower(req.Scenario)
	if scenario == "error" || scenario == "model_error" {
		return llm.Response{}, ErrModel
	}
	step := req.Turn
	completed := 0
	for i, msg := range req.Messages {
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		all := true
		for _, call := range msg.ToolCalls {
			found := false
			for _, result := range req.Messages[i+1:] {
				if result.Role == "tool" && result.ToolCallID == call.ID {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			completed++
		}
	}
	if completed > step {
		step = completed
	}
	text := "Mock assistant: ready."
	var calls []llm.ToolCall
	add := func(name string, args any) {
		raw, _ := json.Marshal(args)
		calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("mock-%d-%d", step, len(calls)), Name: name, Args: raw})
	}
	read := func() { add("read_file", map[string]string{"path": "calc/calc.go"}) }
	parallel := func() { read(); add("read_file", map[string]string{"path": "calc/calc_test.go"}) }
	delegate := func() {
		add("delegate", map[string]string{"path": "./calc", "question": "Inspect Add and its tests; explain the correction without modifying files."})
	}
	write := func() {
		add("write_file", map[string]string{"path": "calc/calc.go", "content": "package calc\n\nfunc Add(a, b int) int { return a + b }\n"})
	}
	command := func() { add("run_command", map[string]any{"cmd": "go", "args": []string{"test", "./..."}}) }
	switch scenario {
	case "demo":
		switch step {
		case 0:
			text = "I will examine calc and its tests."
			parallel()
		case 1:
			text = "I will delegate a read-only inspection of ./calc."
			delegate()
		case 2:
			text = "I propose correcting Add, subject to approval."
			write()
		case 3:
			text = "I will run go test ./... to check the correction."
			command()
		default:
			text = "Calc inspection is complete. The proposed correction adds a and b; review the tool results for write approval and test outcome."
		}
	case "parallel", "parallel_read", "parallel_read_tools", "explorer":
		if step == 0 {
			text = "I will read calc and its tests."
			parallel()
		} else {
			text = "Read-only inspection complete: Add should return a + b."
		}
	case "single", "single_tool":
		if step == 0 {
			text = "I will read calc."
			read()
		} else {
			text = "File inspection complete."
		}
	case "delegate":
		if step == 0 {
			text = "I will request a read-only inspection."
			delegate()
		} else {
			text = "Delegated inspection complete."
		}
	case "write", "approved_write", "write_file":
		if step == 0 {
			text = "I propose correcting Add, subject to approval."
			write()
		} else {
			text = "Write request processed; review the tool result for approval and outcome."
		}
	case "command", "run_command":
		if step == 0 {
			text = "I will run go test ./...."
			command()
		} else {
			text = "Test command processed; review its output for the result."
		}
	}
	res := llm.Response{Message: llm.Message{Role: "assistant", Content: text, ToolCalls: calls}}
	// A word (including its trailing space) is one deterministic mock token.
	words := strings.SplitAfter(text, " ")
	for _, word := range words {
		if err := ctx.Err(); err != nil {
			return llm.Response{}, err
		}
		if emit != nil {
			if err := emit(llm.Delta{Text: word, Tokens: 1}); err != nil {
				return llm.Response{}, err
			}
		}
		res.Tokens++
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return res, nil
}

var _ llm.Model = (*Model)(nil)
