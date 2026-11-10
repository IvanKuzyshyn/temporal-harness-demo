package main

import (
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"io"
	"strings"
	"testing"
)

func TestSummaryPreservesSessionMetadata(t *testing.T) {
	info := &workflows.SessionInfo{Model: "original", Workdir: "/original", DataDir: "/data", Scenario: "demo"}
	got := summary("s", workflows.State{Session: info})
	if got.Session == nil || *got.Session != *info {
		t.Fatalf("lost metadata: %+v", got)
	}
	if summary("legacy", workflows.State{}).Session != nil {
		t.Fatal("invented legacy configuration")
	}
}

// Stop at configuration validation, before any service access, while exercising
// each real command's parser. Root resolution is covered by config tests.
func TestWorkspaceFlagsAllCommands(t *testing.T) {
	t.Setenv("HARNESS_MAX_TURNS", "invalid")
	for _, command := range []string{"run", "tui", "demo"} {
		t.Run(command, func(t *testing.T) {
			args := []string{"--workdir", t.TempDir()}
			var err error
			if command == "run" {
				err = runCommand(append(args, "-p", "hello"), io.Discard)
			} else {
				err = interactive(args, io.Discard, command == "demo")
			}
			if err == nil || !strings.Contains(err.Error(), "HARNESS_MAX_TURNS") {
				t.Fatalf("workdir flag not accepted: %v", err)
			}
		})
	}
}
