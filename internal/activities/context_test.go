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

func TestGatherGitRefusesIndirectRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	external := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", external).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if err := os.WriteFile(external+"/external-secret", []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"symlink", "gitfile"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			var err error
			if kind == "symlink" {
				err = os.Symlink(external+"/.git", dir+"/.git")
			} else {
				err = os.WriteFile(dir+"/.git", []byte("gitdir: "+external+"/.git\n"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if out := gatherGit(context.Background(), dir); out != "Context unavailable: git directory must be a local directory" {
				t.Fatalf("indirect repository accepted: %q", out)
			}
		})
	}
}

func TestGatherGitPinsWorktreeAndIgnoresInheritedGitVariables(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir, external := t.TempDir(), t.TempDir()
	for _, root := range []string{dir, external} {
		if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
			t.Fatalf("git init: %s %v", out, err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "config", "core.worktree", external).CombinedOutput(); err != nil {
		t.Fatalf("git config: %s %v", out, err)
	}
	for path, content := range map[string]string{dir + "/visible": "hello", external + "/external-secret": "private"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T) {
		t.Helper()
		if out := gatherGit(context.Background(), dir); out != "?? visible\n" {
			t.Fatalf("status escaped pinned root: %q", out)
		}
	}
	t.Run("local config override", check)
	t.Run("inherited overrides", func(t *testing.T) {
		t.Setenv("GIT_DIR", external+"/.git")
		t.Setenv("GIT_WORK_TREE", external)
		t.Setenv("GIT_COMMON_DIR", external+"/.git")
		t.Setenv("GIT_INDEX_FILE", external+"/.git/index")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
		t.Setenv("GIT_CONFIG_VALUE_0", external)
		check(t)
	})
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
