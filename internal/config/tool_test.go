package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func toolScript(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveTool(t *testing.T) {
	dir := t.TempDir()
	tool := toolScript(t, filepath.Join(t.TempDir(), "tool with spaces"), "exit 0\n")
	t.Setenv("PATH", dir)
	toolScript(t, filepath.Join(dir, "mise"), "[ \"$#\" = 2 ] && [ \"$1\" = which ] && [ \"$2\" = temporal ] || exit 9\nprintf '%s\\n' '"+tool+"'\n")
	got, err := ResolveTool(context.Background(), "temporal")
	if err != nil || got != tool {
		t.Fatalf("got %q, %v", got, err)
	}
	preferred := toolScript(t, filepath.Join(dir, "temporal"), "exit 0\n")
	toolScript(t, filepath.Join(dir, "mise"), "exit 99\n")
	got, err = ResolveTool(context.Background(), "temporal")
	if err != nil || got != preferred {
		t.Fatalf("PATH not preferred: %q, %v", got, err)
	}
}

func TestResolveToolBadOutput(t *testing.T) {
	nonexec := filepath.Join(t.TempDir(), "nonexec")
	if err := os.WriteFile(nonexec, nil, 0644); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"", "relative/tool", "/missing/tool", nonexec, t.TempDir(), "/bin/sh\n/bin/sh"} {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			toolScript(t, filepath.Join(dir, "mise"), "printf '%s\\n' '"+output+"'\n")
			if _, err := ResolveTool(context.Background(), "temporal"); err == nil {
				t.Fatal("accepted bad output")
			}
		})
	}
}

func TestResolveToolErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := ResolveTool(context.Background(), "temporal"); err == nil || !strings.Contains(err.Error(), "mise") {
		t.Fatalf("missing tools: %v", err)
	}
	toolScript(t, filepath.Join(dir, "mise"), "echo not-installed >&2\nexit 7\n")
	if _, err := ResolveTool(context.Background(), "temporal"); err == nil || !strings.Contains(err.Error(), "not-installed") {
		t.Fatalf("mise failure: %v", err)
	}
	toolScript(t, filepath.Join(dir, "mise"), "exec /bin/sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ResolveTool(ctx, "temporal"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("resolution did not stop promptly")
	}
	start = time.Now()
	if _, err := ResolveTool(context.Background(), "temporal"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("default bound: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 5*time.Second {
		t.Fatalf("unexpected default lookup duration: %s", elapsed)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveTool(ctx, "temporal"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
