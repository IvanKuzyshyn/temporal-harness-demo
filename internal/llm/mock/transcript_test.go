package mock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

func TestIncompleteRoundAndExplorer(t *testing.T) {
	req := llm.Request{Scenario: "demo"}
	first, _ := New().Chat(context.Background(), req, nil)
	req.Messages = []llm.Message{first.Message, {Role: "tool", ToolCallID: first.Message.ToolCalls[0].ID}}
	next, _ := New().Chat(context.Background(), req, nil)
	if len(next.Message.ToolCalls) != 2 || next.Message.ToolCalls[0].Name != "read_file" {
		t.Fatal("advanced before both results", next)
	}
	req.Messages = append(req.Messages, llm.Message{Role: "tool", ToolCallID: first.Message.ToolCalls[1].ID})
	req.Scenario = "explorer"
	for turn := 0; turn < 6; turn++ {
		req.Turn = turn
		res, _ := New().Chat(context.Background(), req, nil)
		for _, call := range res.Message.ToolCalls {
			if call.Name != "read_file" {
				t.Fatal("explorer not read-only", call)
			}
		}
	}
}

func TestAliasesAndArguments(t *testing.T) {
	for _, scenario := range []string{"parallel_read", "parallel_read_tools", "single_tool", "approved_write", "write_file", "run_command", "model_error", "basic_chat", "unknown"} {
		res, err := New().Chat(context.Background(), llm.Request{Scenario: scenario}, nil)
		if scenario == "model_error" {
			if err != ErrModel {
				t.Fatal(err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range res.Message.ToolCalls {
			var args map[string]any
			if err := json.Unmarshal(call.Args, &args); err != nil {
				t.Fatal(err)
			}
			switch call.Name {
			case "write_file":
				if args["path"] != "calc/calc.go" || args["content"] != "package calc\n\nfunc Add(a, b int) int { return a + b }\n" {
					t.Fatal(args)
				}
			case "run_command":
				if args["cmd"] != "go" {
					t.Fatal(args)
				}
				a := args["args"].([]any)
				if len(a) != 2 || a[0] != "test" || a[1] != "./..." {
					t.Fatal(args)
				}
			}
		}
	}
}
