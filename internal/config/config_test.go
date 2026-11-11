package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	for _, k := range []string{"HARNESS_MODEL", "TEMPORAL_ADDRESS", "OLLAMA_HOST", "HARNESS_WORKDIR", "HARNESS_DATA_DIR", "HARNESS_APPROVAL_TIMEOUT", "HARNESS_HANG_TIMEOUT", "HARNESS_MAX_TURNS"} {
		t.Setenv(k, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "gemma4:e4b" || c.TemporalAddress != "localhost:7233" || c.OllamaHost != "127.0.0.1:11434" || c.MaxTurns != 12 || c.ApprovalTimeout != 5*time.Minute || c.HangTimeout != 120*time.Second {
		t.Fatalf("defaults: %+v", c)
	}
	for _, p := range []string{c.Workdir, c.DataDir} {
		if !filepath.IsAbs(p) {
			t.Fatal(p)
		}
	}
}
func TestEnvironment(t *testing.T) {
	t.Setenv("HARNESS_MODEL", "mock")
	t.Setenv("HARNESS_WORKDIR", "project")
	t.Setenv("HARNESS_DATA_DIR", "state")
	t.Setenv("HARNESS_MAX_TURNS", "4")
	t.Setenv("HARNESS_APPROVAL_TIMEOUT", "3s")
	t.Setenv("HARNESS_HANG_TIMEOUT", "2s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "mock" || c.MaxTurns != 4 || c.ApprovalTimeout != 3*time.Second || c.HangTimeout != 2*time.Second || filepath.Base(c.DataDir) != "state" {
		t.Fatalf("env: %+v", c)
	}
}
func TestInvalidEnvironment(t *testing.T) {
	for _, k := range []string{"HARNESS_MAX_TURNS", "HARNESS_APPROVAL_TIMEOUT", "HARNESS_HANG_TIMEOUT"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, "-1")
			if _, err := Load(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
