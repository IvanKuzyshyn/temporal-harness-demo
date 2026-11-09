package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ResolveTool prefers the caller's PATH, then queries mise for an already
// installed tool. It never installs tools, activates a shell, or changes state.
// The lookup is bounded even when the caller supplies no deadline.
func ResolveTool(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if name == "" || strings.ContainsAny(name, "/\\\x00\r\n") || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("invalid tool name %q", name)
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		return "", fmt.Errorf("resolve %s: not on PATH and mise unavailable; install the tool or install mise and run mise install: %w", name, err)
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, mise, "which", name)
	// Descendants must not hold output pipes open beyond the lookup deadline.
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr toolOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if c.Err() != nil {
		return "", fmt.Errorf("resolve %s via mise which: %w", name, c.Err())
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s via mise which (run mise install for configured tools): %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	path := strings.TrimSpace(stdout.String())
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("mise which %s returned invalid absolute executable path %q", name, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("mise which %s returned unusable path %q: %w", name, path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("mise which %s returned non-executable file %q", name, path)
	}
	if _, err := exec.LookPath(path); err != nil {
		return "", fmt.Errorf("mise which %s returned unusable executable %q: %w", name, path, err)
	}
	return path, nil
}

// Bound diagnostic/output memory as well as lookup time.
type toolOutput struct{ buffer bytes.Buffer }

func (b *toolOutput) String() string { return b.buffer.String() }

func (b *toolOutput) Write(p []byte) (int, error) {
	if len(p) > 64*1024-b.buffer.Len() {
		return 0, fmt.Errorf("mise output exceeds 64 KiB")
	}
	return b.buffer.Write(p)
}
