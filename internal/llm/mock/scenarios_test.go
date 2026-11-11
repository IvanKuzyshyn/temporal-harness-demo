package mock

import (
	"context"
	"errors"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"reflect"
	"testing"
)

func TestScenarios(t *testing.T) {
	cases := map[string][]string{"chat": {}, "parallel": {"read_file", "read_file"}, "single": {"read_file"}, "delegate": {"delegate"}, "write": {"write_file"}, "command": {"run_command"}, "explorer": {"read_file", "read_file"}, "demo": {"read_file", "read_file"}}
	for scenario, names := range cases {
		t.Run(scenario, func(t *testing.T) {
			req := llm.Request{Scenario: scenario}
			var text string
			var tokens int
			res, err := New().Chat(context.Background(), req, func(d llm.Delta) error { text += d.Text; tokens += d.Tokens; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Message.ToolCalls) != len(names) || text != res.Message.Content || tokens != res.Tokens || tokens == 0 {
				t.Fatalf("%+v", res)
			}
			for i, name := range names {
				if res.Message.ToolCalls[i].Name != name {
					t.Fatal(res)
				}
			}
			again, _ := New().Chat(context.Background(), req, nil)
			if !reflect.DeepEqual(res, again) {
				t.Fatal("nondeterministic")
			}
			if scenario != "demo" {
				req.Turn = 1
				last, _ := New().Chat(context.Background(), req, nil)
				if len(last.Message.ToolCalls) != 0 {
					t.Fatal(last)
				}
			}
		})
	}
}
func TestDemoTranscript(t *testing.T) {
	req := llm.Request{Scenario: "demo"}
	for _, names := range [][]string{{"read_file", "read_file"}, {"delegate"}, {"write_file"}, {"run_command"}, {}} {
		res, err := New().Chat(context.Background(), req, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Message.ToolCalls) != len(names) {
			t.Fatalf("%+v", res)
		}
		req.Messages = append(req.Messages, res.Message)
		for i, name := range names {
			call := res.Message.ToolCalls[i]
			if call.Name != name {
				t.Fatal(call)
			}
			req.Messages = append(req.Messages, llm.Message{Role: "tool", ToolCallID: call.ID, ToolName: call.Name, Content: "success"})
		}
	}
}
func TestFailures(t *testing.T) {
	_, err := New().Chat(context.Background(), llm.Request{Scenario: "error"}, nil)
	if !errors.Is(err, ErrModel) {
		t.Fatal(err)
	}
	boom := errors.New("emit")
	_, err = New().Chat(context.Background(), llm.Request{}, func(llm.Delta) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = New().Chat(ctx, llm.Request{}, func(llm.Delta) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
