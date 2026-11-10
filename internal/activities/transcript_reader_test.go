package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

func pageFixture(t *testing.T, dir, id string, n int) []transcriptEntry {
	t.Helper()
	entries := make([]transcriptEntry, n)
	for i := range entries {
		entries[i] = transcriptEntry{ID: fmt.Sprintf("%064x", i+1), Message: llm.Message{Role: "user", Content: fmt.Sprint(i)}}
	}
	writePageFixture(t, dir, id, entries)
	return entries
}
func writePageFixture(t *testing.T, dir, id string, entries []transcriptEntry) {
	t.Helper()
	path := filepath.Join(dir, TranscriptRef(id))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestTranscriptPageBoundariesAndAppends(t *testing.T) {
	dir := t.TempDir()
	id := "../../opaque/session"
	pageFixture(t, dir, id, 130)
	p, err := ReadTranscriptPage(context.Background(), dir, id, 0, 999)
	if err != nil || len(p.Entries) != 64 || p.Before != 66 || !p.HasMore || p.Total != 130 {
		t.Fatalf("%+v %v", p, err)
	}
	pageFixture(t, dir, id, 133)
	p, err = ReadTranscriptPage(context.Background(), dir, id, p.Before, 64)
	if err != nil || p.Before != 2 || p.Entries[63].Message.Content != "65" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = ReadTranscriptPage(context.Background(), dir, id, p.Before, 64)
	if err != nil || p.Before != 0 || p.HasMore || len(p.Entries) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, before := range []int{-1, 134} {
		if _, err := ReadTranscriptPage(context.Background(), dir, id, before, 64); err == nil {
			t.Fatal("accepted invalid cursor")
		}
	}
	if _, err := ReadTranscriptPage(context.Background(), dir, id, 0, 0); err == nil {
		t.Fatal("accepted zero limit")
	}
}
func TestTranscriptPageValidation(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"empty", "[]", true}, {"null", "null", false}, {"malformed", "[", false}, {"trailing", "[] []", false},
		{"missing identity", `[{"message":{"role":"user"}}]`, false},
		{"invalid UTF-8", fmt.Sprintf(`[{"id":"%064x","message":{"role":"user","content":"%s"}}]`, 1, "\xff"), false},
		{"unknown role", fmt.Sprintf(`[{"id":"%064x","message":{"role":"invalid"}}]`, 1), false},
		{"ambiguous calls", fmt.Sprintf(`[{"id":"%064x","message":{"role":"assistant","toolCalls":[{"id":"same"},{"id":"same"}]}}]`, 1), false},
		{"bad identity", `[{"id":"escape","message":{"role":"user"}}]`, false},
		{"oversized", strings.Repeat(" ", maxTranscriptBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pageFixture(t, dir, "s", 0)
			if err := os.WriteFile(filepath.Join(dir, TranscriptRef("s")), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 64)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
	dir := t.TempDir()
	entries := pageFixture(t, dir, "s", 2)
	entries[1].ID = entries[0].ID
	writePageFixture(t, dir, "s", entries)
	if _, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 64); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err := ReadTranscriptPage(context.Background(), dir, "missing", 0, 64); err == nil {
		t.Fatal("missing artifact must be unavailable, not certified empty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadTranscriptPage(ctx, dir, "s", 0, 64); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := ReadTranscriptPage(context.Background(), "relative", "s", 0, 64); err == nil {
		t.Fatal("relative data dir accepted")
	}
}

// A deterministic context that cancels during validation, not just at entry.
type transcriptCancelContext struct {
	context.Context
	checks int
}

func (c *transcriptCancelContext) Err() error {
	c.checks++
	if c.checks > 10 {
		return context.Canceled
	}
	return nil
}
func TestTranscriptPageResourceBoundsAndCompleteMessages(t *testing.T) {
	dir := t.TempDir()
	entries := pageFixture(t, dir, "s", 100)
	entries[99].Message.Content = strings.Repeat("complete\n", 16000)
	entries[99].Message.ToolCalls = []llm.ToolCall{{ID: "call", Name: "read_file", Args: json.RawMessage(`{"path":"README.md"}`)}}
	writePageFixture(t, dir, "s", entries)
	p, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 1)
	if err != nil || len(p.Entries) != 1 || p.Entries[0].Message.Content != entries[99].Message.Content || string(p.Entries[0].Message.ToolCalls[0].Args) != string(entries[99].Message.ToolCalls[0].Args) {
		t.Fatalf("message truncated: %v", err)
	}
	ctx := &transcriptCancelContext{Context: context.Background()}
	if _, err := ReadTranscriptPage(ctx, dir, "s", 0, 64); err != context.Canceled {
		t.Fatalf("cancellation during read/validation: %v", err)
	}
	// Invalid identity outside the requested window must still invalidate it.
	entries[0].ID = "invalid"
	writePageFixture(t, dir, "s", entries)
	if _, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 1); err == nil {
		t.Fatal("only requested page validated")
	}
	pageFixture(t, dir, "s", maxTranscriptRecords+1)
	if _, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 64); err == nil {
		t.Fatal("record bound not enforced")
	}
}
func TestTranscriptPageRejectsSpecialFilesAndArtifactSiblingEscape(t *testing.T) {
	for _, kind := range []string{"directory", "fifo", "sibling"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, TranscriptRef("s"))
			if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "sibling":
				if err := os.WriteFile(filepath.Join(dir, "sibling.json"), []byte("[]"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../sibling.json", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 64); err == nil {
				t.Fatal("unsafe artifact accepted")
			}
		})
	}
}
func TestTranscriptPageRejectsSymlinkEscapes(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			pageFixture(t, outside, "s", 1)
			if directory {
				if err := os.Symlink(filepath.Join(outside, "artifacts"), filepath.Join(dir, "artifacts")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(dir, "artifacts"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, TranscriptRef("s")), filepath.Join(dir, TranscriptRef("s"))); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadTranscriptPage(context.Background(), dir, "s", 0, 64); err == nil {
				t.Fatal("escape accepted")
			}
		})
	}
}
