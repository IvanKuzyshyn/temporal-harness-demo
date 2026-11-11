package activities

import (
	"context"
	"os"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestToolPreflightAndExecution(t *testing.T) {
	a := &Activities{}
	dir := t.TempDir()
	os.WriteFile(dir+"/README.md", []byte("hello"), 0600)
	for _, tc := range []struct {
		name, args        string
		allowed, approval bool
	}{
		{"read_file", `{"path":"README.md"}`, true, false},
		{"write_file", `{"path":"new","content":"x"}`, true, true},
		{"read_file", `{"path":"../escape"}`, false, false},
		{"unknown", `{}`, false, false},
	} {
		in := workflows.ToolInput{Workdir: dir, Call: llm.ToolCall{ID: "id", Name: tc.name, Args: []byte(tc.args)}}
		p, err := a.PreflightTool(context.Background(), in)
		if err != nil || p.Allowed != tc.allowed || p.RequiresApproval != tc.approval {
			t.Fatalf("%s: %+v %v", tc.name, p, err)
		}
		if p.Allowed && p.ActionHash == "" {
			t.Fatal("missing hash")
		}
	}
	result, err := a.ExecuteTool(context.Background(), workflows.ToolInput{Workdir: dir, Call: llm.ToolCall{ID: "read", Name: "read_file", Args: []byte(`{"path":"README.md"}`)}})
	if err != nil || result.IsError || result.Output != "hello" || result.ToolCallID != "read" {
		t.Fatalf("%+v %v", result, err)
	}
}
