package activities

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestGatherContextSnapshots(t *testing.T) {
	a := &Activities{}
	dir, data := t.TempDir(), t.TempDir()
	os.WriteFile(dir+"/README.md", []byte(strings.Repeat("TODO hello\n", 2000)), 0600)
	scope := events.Scope{WorkflowID: "parent", RunID: "run"}
	var wg sync.WaitGroup
	for _, kind := range []string{"git", "tree", "readme", "todo"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := workflows.GatherInput{Scope: scope, DataDir: data, Workdir: dir, Kind: kind}
			result, err := a.GatherContext(context.Background(), in)
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := json.Marshal(result)
			if len(b) > 256 || result.TranscriptRef != TranscriptRef("parent") {
				t.Errorf("non-reference history: %s", b)
			}
		}()
	}
	wg.Wait()
	messages, err := ReadTranscript(data, TranscriptRef("parent"))
	if err != nil || len(messages) != 4 {
		t.Fatalf("%v %v", messages, err)
	}
	for _, m := range messages {
		if len(m.Content) > 8256 || m.Role != "system" {
			t.Fatal("unbounded context")
		}
	}
	os.Remove(dir + "/README.md")
	for _, kind := range []string{"git", "tree", "readme", "todo"} {
		if _, err := a.GatherContext(context.Background(), workflows.GatherInput{Scope: scope, DataDir: data, Workdir: dir, Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := ReadTranscript(data, TranscriptRef("parent"))
	beforeJSON, _ := json.Marshal(messages)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("retry changed snapshot")
	}
}

func TestGatherGitIsScopedAndDisablesFSMonitor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	git("init", "-q")
	marker := dir + "/monitor-ran"
	script := dir + "/monitor"
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	git("config", "core.fsmonitor", script)
	os.Mkdir(dir+"/.data", 0700)
	os.WriteFile(dir+"/.data/secret", []byte("private"), 0600)
	os.WriteFile(dir+"/visible", []byte("hello"), 0600)
	out := gatherGit(context.Background(), dir)
	if strings.Contains(out, "Context unavailable") || !strings.Contains(out, "visible") || strings.Contains(out, ".data") {
		t.Fatal(out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("fsmonitor executed")
	}
	sub := dir + "/sub"
	os.Mkdir(sub, 0700)
	if out := gatherGit(context.Background(), sub); !strings.Contains(out, "not a git repository") {
		t.Fatal("discovered parent repo:", out)
	}
}

func TestGatherMissingContext(t *testing.T) {
	a := &Activities{}
	dir, data := t.TempDir(), t.TempDir()
	for _, kind := range []string{"git", "readme"} {
		_, err := a.GatherContext(context.Background(), workflows.GatherInput{Scope: events.Scope{WorkflowID: "missing"}, DataDir: data, Workdir: dir, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
	}
	messages, _ := ReadTranscript(data, TranscriptRef("missing"))
	if len(messages) != 2 {
		t.Fatal(messages)
	}
	for _, m := range messages {
		if !strings.Contains(m.Content, "Context unavailable") {
			t.Fatal(m)
		}
	}
}
