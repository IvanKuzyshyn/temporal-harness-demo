// Package config provides environment defaults. Exported fields may be bound to
// CLI flags after Load; callers should resolve flag-supplied paths as well.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Model           string
	TemporalAddress string
	OllamaHost      string
	Workdir         string
	DataDir         string
	ApprovalTimeout time.Duration
	HangTimeout     time.Duration
	MaxTurns        int
}

func Load() (Config, error) { return LoadWithWorkdir("") }

// LoadWithWorkdir resolves flag > environment > invocation directory, without I/O
// beyond obtaining the current directory. It does not create or copy a project.
func LoadWithWorkdir(workdir string) (Config, error) {
	c := Config{Model: env("HARNESS_MODEL", "gemma4:e4b"), TemporalAddress: env("TEMPORAL_ADDRESS", "localhost:7233"), OllamaHost: env("OLLAMA_HOST", "127.0.0.1:11434")}
	var err error
	if workdir == "" {
		workdir = env("HARNESS_WORKDIR", ".")
	}
	if c.Workdir, err = filepath.Abs(workdir); err != nil {
		return c, fmt.Errorf("workdir: %w", err)
	}
	if c.DataDir, err = filepath.Abs(env("HARNESS_DATA_DIR", ".data")); err != nil {
		return c, fmt.Errorf("data dir: %w", err)
	}
	for _, v := range []struct {
		key, fallback string
		dest          *time.Duration
	}{{"HARNESS_APPROVAL_TIMEOUT", "5m", &c.ApprovalTimeout}, {"HARNESS_HANG_TIMEOUT", "120s", &c.HangTimeout}} {
		d, e := time.ParseDuration(env(v.key, v.fallback))
		if e != nil || d <= 0 {
			return c, fmt.Errorf("%s must be a positive duration", v.key)
		}
		*v.dest = d
	}
	c.MaxTurns, err = strconv.Atoi(env("HARNESS_MAX_TURNS", "12"))
	if err != nil || c.MaxTurns <= 0 {
		return c, fmt.Errorf("HARNESS_MAX_TURNS must be a positive integer")
	}
	return c, nil
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
