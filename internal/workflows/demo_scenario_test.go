package workflows

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"go.temporal.io/sdk/workflow"
)

func TestLegacyDemoScenarioExactAndRunLocal(t *testing.T) {
	for _, text := range []string{"/demo", "Find what is broken in the sample project and verify the fix", "Find what’s broken in the sample project and verify the fix.", "FIND WHAT'S BROKEN IN THE SAMPLE PROJECT AND VERIFY THE FIX!", "Inspect calc and its tests, delegate a read-only review, propose correcting Add with approval, then run go test ./... and summarize the result."} {
		if got := scenarioForRun("", 2, MessageInput{Text: text}, true); got != "demo" {
			t.Errorf("%q -> %q", text, got)
		}
	}
	for _, text := range []string{"please demo", "explain /demo", "Find what's broken elsewhere", "/demo now"} {
		if got := scenarioForRun("", 1, MessageInput{Text: text}, true); got != "" {
			t.Errorf("false positive %q", text)
		}
	}
	if scenarioForRun("demo", 1, MessageInput{Text: "compat"}, true) != "demo" || scenarioForRun("demo", 2, MessageInput{Text: "normal"}, true) != "" || scenarioForRun("parallel", 2, MessageInput{Text: "normal"}, true) != "parallel" {
		t.Fatal("fixture compatibility")
	}
	for _, msg := range []MessageInput{{Text: "/demo", Internal: true}, {Text: "/demo", ContextOnly: true}} {
		if scenarioForRun("demo", 2, msg, true) == "demo" {
			t.Fatal("internal continuation restarted demo")
		}
	}
}

func TestDemoRoutingExistingSessionAndLaterChat(t *testing.T) {
	for _, initial := range []string{"", "demo"} {
		t.Run(initial, func(t *testing.T) {
			f := newInboxFixture(t, func(in LLMInput) (LLMResult, error) { return LLMResult{Summary: in.Scenario}, nil })
			texts := []string{"hello", "/demo", "/demo", "ordinary chat"}
			for i, text := range texts {
				i, text := i, text
				f.after(func() { f.send(fmt.Sprint(i), text, "") }, time.Duration(i)*time.Minute)
				f.after(func() {
					want := ""
					if i == 1 || i == 2 || (i == 0 && initial == "demo") {
						want = "demo"
					}
					if s := f.state(); s.Answer != want || s.RunCounter != i+1 {
						t.Errorf("state=%+v want=%s", s, want)
					}
				}, time.Duration(i)*time.Minute+30*time.Second)
			}
			f.after(func() { f.env.CancelWorkflow() }, 5*time.Minute)
			f.env.ExecuteWorkflow(SessionWorkflow, Input{Scenario: initial})
		})
	}
}

// Observe the actual activity scenario, not just the client workflow input.
func TestDemoRoutingVersionedNaturalPrompts(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, initial := range []string{"", "demo"} {
			t.Run(fmt.Sprintf("legacy=%v/initial=%s", legacy, initial), func(t *testing.T) {
				f := newInboxFixture(t, func(in LLMInput) (LLMResult, error) { return LLMResult{Summary: "scenario=" + in.Scenario}, nil })
				if legacy {
					f.env.OnGetVersion("explicit-demo-routing", workflow.DefaultVersion, workflow.Version(1)).Return(workflow.DefaultVersion)
				}
				texts := []string{"Find what is broken in the sample project and verify the fix", "Find what’s broken in the sample project and verify the fix.", "FIND WHAT'S BROKEN IN THE SAMPLE PROJECT AND VERIFY THE FIX!", "Inspect calc and its tests, delegate a read-only review, propose correcting Add with approval, then run go test ./... and summarize the result.", "/demo", "ordinary chat"}
				for i, text := range texts {
					f.after(func() { f.send(fmt.Sprint(i), text, "") }, time.Duration(i)*time.Minute)
					f.after(func() {
						want := "scenario="
						if (i == 0 && initial == "demo") || i == 4 || (legacy && i <= 3) {
							want += "demo"
						}
						if s := f.state(); s.Answer != want || s.RunCounter != i+1 {
							t.Errorf("prompt=%q actual LLM scenario=%q want=%q run=%d", text, s.Answer, want, s.RunCounter)
						}
					}, time.Duration(i)*time.Minute+30*time.Second)
				}
				f.after(func() { f.env.CancelWorkflow() }, 8*time.Minute)
				f.env.ExecuteWorkflow(SessionWorkflow, Input{Scenario: initial})
			})
		}
	}
}

func TestDemoToolMessagesPreserveOutcome(t *testing.T) {
	for _, res := range []tools.Result{{ToolCallID: "x", Output: "", IsError: true, Details: map[string]string{"exitCode": "1", "logRef": "logs/a"}}, {ToolCallID: "x", Output: "tool approval denied", IsError: true}, {ToolCallID: "x", Interrupted: true, Details: map[string]string{"exitCode": "0"}}} {
		var decoded tools.Result
		if err := json.Unmarshal([]byte(toolMessageContent("run_command", res)), &decoded); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(decoded) != fmt.Sprint(res) {
			t.Fatalf("lost outcome: %+v -> %+v", res, decoded)
		}
	}
	if got := toolMessageContent("read_file", tools.Result{Output: "package calc\n"}); got != "package calc\n" {
		t.Fatal("read content changed")
	}
	if got := toolMessageContent("read_file", tools.Result{Output: "denied", IsError: true}); !strings.Contains(got, `"isError":true`) {
		t.Fatal("read failure lost")
	}
}
