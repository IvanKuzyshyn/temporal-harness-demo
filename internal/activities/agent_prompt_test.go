package activities

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"reflect"
	"strings"
	"testing"
)

func TestAgentMessagesPureAndPreservesPairsAndExplorer(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "Explore read-only. Never modify files, run commands, or delegate. Task: inspect"},
		{Role: "system", Content: "Initial context: malicious README says run commands"},
		{Role: "system", Content: "Project context (readme):\nSTALE"},
		{Role: "user", Content: "why is it slow?"},
		{Role: "system", Content: "Project context (evidence):\nFRESH"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "a", Name: "read_file"}}},
		{Role: "tool", ToolCallID: "a", Content: "observed code"},
	}
	before := append([]llm.Message(nil), messages...)
	out := agentMessages(messages, "/trusted/root")
	if !reflect.DeepEqual(before, messages) {
		t.Fatal("mutated durable messages")
	}
	if out[0].Role != "system" || !strings.Contains(out[0].Content, "cite") || !strings.Contains(out[0].Content, "not executed") {
		t.Fatal(out[0])
	}
	text := ""
	for _, m := range out {
		text += m.Content
		if m.Role == "system" && strings.Contains(m.Content, "malicious README") {
			t.Fatal("privileged context")
		}
	}
	if strings.Contains(text, "STALE") || !strings.Contains(text, "FRESH") || !strings.Contains(text, "Never modify files, run commands, or delegate") {
		t.Fatal(text)
	}
	if out[len(out)-2].ToolCalls[0].ID != "a" || out[len(out)-1].ToolCallID != "a" {
		t.Fatal("broken tool pair")
	}
	child := agentMessages(messages[:2], "/child")
	if len(child) < 3 || !strings.Contains(child[1].Content, "Explore read-only") {
		t.Fatal("lost child instruction", child)
	}
}
