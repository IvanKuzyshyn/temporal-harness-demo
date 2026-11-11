package activities

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestApprovalPreviewRealFiles(t *testing.T) {
	cases := []struct {
		name, old, proposed string
		missing             bool
		want                []string
	}{
		{"replace", "old\n", "new\n", false, []string{"-old\n", "+new\n"}},
		{"new", "", "created\n", true, []string{"new file", "+created\n"}},
		{"unchanged", "same\n", "same\n", false, []string{"unchanged"}},
		{"empty", "old", "", false, []string{"-old\n", "proposed empty", "No newline at end of current"}},
		{"unicode", "雪\n\n終", "🌍\n新", false, []string{"-雪\n-\n-終\n", "+🌍\n+新\n", "No newline at end of current", "No newline at end of proposed"}},
		{"large old", strings.Repeat("雪", 10000), "visible proposal\n", false, []string{"current truncated", "+visible proposal\n"}},
		{"large proposed", "old\n", strings.Repeat("🌍", 10000), false, []string{"-old\n", "+🌍", "proposed truncated"}},
		{"both large", strings.Repeat("a\n", 10000), strings.Repeat("b\n", 10000), false, []string{"-a\n", "+b\n", "current truncated", "proposed truncated"}},
		{"same large prefix", strings.Repeat("x", 9000), strings.Repeat("x", 9000), false, []string{"current truncated", "proposed truncated"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "file")
			if !tc.missing {
				if err := os.WriteFile(target, []byte(tc.old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args, _ := json.Marshal(map[string]string{"path": "file", "content": tc.proposed})
			call := llm.ToolCall{Name: "write_file", Args: args}
			wantHash, err := tools.ActionHash(call)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (&Activities{}).PreflightTool(context.Background(), workflows.ToolInput{Workdir: root, Call: call})
			if err != nil || !got.Allowed || !got.RequiresApproval {
				t.Fatalf("preflight: %+v %v", got, err)
			}
			if got.ActionHash != wantHash {
				t.Fatal("hash changed")
			}
			if len(got.Preview) > 8192 || !utf8.ValidString(got.Preview) {
				t.Fatalf("invalid preview, bytes=%d", len(got.Preview))
			}
			for _, want := range append(tc.want, "--- current", "+++ proposed") {
				if !strings.Contains(got.Preview, want) {
					t.Fatalf("missing %q in %q", want, got.Preview)
				}
			}
			data, err := os.ReadFile(target)
			if tc.missing {
				if !os.IsNotExist(err) {
					t.Fatal("preflight created file")
				}
			} else if err != nil || string(data) != tc.old {
				t.Fatal("preflight mutated file")
			}
		})
	}
}

func TestApprovalPreviewValidationFirst(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := "OUTSIDE_SECRET_MUST_NOT_LEAK"
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"path":"link","content":"new"}`, `{"path":"../secret","content":"new"}`, `{"path":"link","content":7}`, `{"path":"file","content":"a","content":"b"}`} {
		got, err := (&Activities{}).PreflightTool(context.Background(), workflows.ToolInput{Workdir: root, Call: llm.ToolCall{Name: "write_file", Args: json.RawMessage(args)}})
		if err != nil || got.Allowed || got.Preview != "" || got.ActionHash != "" || strings.Contains(got.Reason, secret) {
			t.Fatalf("unsafe result: %+v %v", got, err)
		}
	}
}

func TestApprovalPreviewUnavailable(t *testing.T) {
	// Exercise a read failure after validation without permission assumptions
	// (tests may run as root): the helper must not claim a deletion or equality.
	root := t.TempDir()
	args := json.RawMessage(`{"path":"file","content":"still visible"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	preview := writeApprovalPreview(ctx, workflows.ToolInput{Workdir: root, Call: llm.ToolCall{Name: "write_file", Args: args}})
	if !strings.Contains(preview, "+still visible") {
		t.Fatal(preview)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	preview = writeApprovalPreview(ctx, workflows.ToolInput{Workdir: root, Call: llm.ToolCall{Name: "write_file", Args: args}})
	if !strings.Contains(preview, "current unreadable/unavailable") || !strings.Contains(preview, "+still visible") {
		t.Fatal(preview)
	}
}

func TestApprovalPreviewOtherToolAndFullHash(t *testing.T) {
	root := t.TempDir()
	args := json.RawMessage(`{"cmd":"go","args":["test","./..."]}`)
	call := llm.ToolCall{Name: "run_command", Args: args}
	got, err := (&Activities{}).PreflightTool(context.Background(), workflows.ToolInput{Workdir: root, Call: call})
	if err != nil || !got.Allowed || got.Preview != "run_command "+string(args) {
		t.Fatalf("%+v %v", got, err)
	}
	var previous string
	for _, tail := range []string{"a", "b"} {
		raw, _ := json.Marshal(map[string]string{"path": "new", "content": strings.Repeat("x", 20000) + tail})
		call = llm.ToolCall{Name: "write_file", Args: raw}
		got, err = (&Activities{}).PreflightTool(context.Background(), workflows.ToolInput{Workdir: root, Call: call})
		hash, _ := tools.ActionHash(call)
		if err != nil || got.ActionHash != hash || got.ActionHash == previous {
			t.Fatal("full arguments not hashed")
		}
		previous = got.ActionHash
	}
}
