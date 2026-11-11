package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
)

func TestRunAuthoritativeConfigurationDiagnostics(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		for _, kind := range []string{"matching", "mismatch", "legacy", "new", "sanitized"} {
			t.Run(kind+map[bool]string{true: "/json", false: "/plain"}[asJSON], func(t *testing.T) {
				cfg := config.Config{Model: "original-model", Workdir: "/original-root", DataDir: t.TempDir()}
				info := &workflows.SessionInfo{Model: cfg.Model, Workdir: cfg.Workdir, DataDir: cfg.DataDir}
				switch kind {
				case "mismatch":
					cfg.Model, cfg.Workdir = "client-model", "/client-root"
				case "legacy":
					info = nil
				case "sanitized":
					info.Model = "model\x1b[31m\nforged\r\t\u009b"
					info.Workdir = "/root\nforged\x00"
				}
				f := &cliFake{states: []workflows.State{{Status: "idle", Session: info}, {Status: "idle", RunCounter: 1, Session: info, Answer: "answer"}}}
				if kind == "new" {
					f.queryErrors = map[int]error{0: serviceerror.NewNotFound("new")}
				}
				var out, diag bytes.Buffer
				f.submit = func() {
					if kind == "new" {
						if diag.Len() != 0 {
							t.Errorf("claimed new configuration before query: %q", diag.String())
						}
					} else if diag.Len() == 0 {
						t.Error("missing pre-submit baseline diagnostic")
					}
				}
				err := runSession(context.Background(), f, cfg, runOptions{session: "s", prompt: "hello", json: asJSON, timeout: time.Second}, &out, &diag, time.Millisecond)
				if err != nil {
					t.Fatal(err)
				}
				text := diag.String()
				if kind == "legacy" {
					if !strings.Contains(text, "unknown (legacy") || strings.Contains(text, "original-model") {
						t.Fatalf("legacy misrepresented: %q", text)
					}
				} else {
					if !strings.Contains(text, "original session") {
						t.Errorf("missing authoritative label: %q", text)
					}
					if kind != "sanitized" && (!strings.Contains(text, "original-model") || !strings.Contains(text, "/original-root")) {
						t.Errorf("missing original model/root: %q", text)
					}
					wantMismatch := kind == "mismatch" || kind == "sanitized"
					if strings.Contains(text, "mismatch") != wantMismatch {
						t.Errorf("wrong mismatch diagnostic: %q", text)
					}
					if kind == "mismatch" && !strings.Contains(text, "client selection not applied") {
						t.Errorf("ambiguous mismatch: %q", text)
					}
				}
				for _, r := range strings.TrimSuffix(text, "\n") {
					if unicode.IsControl(r) && r != '\n' {
						t.Errorf("unsafe diagnostic: %q", text)
						break
					}
				}
				if strings.Contains(text, "\nforged") {
					t.Errorf("multiline metadata injected: %q", text)
				}
				if asJSON {
					for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
						if !json.Valid(line) {
							t.Fatalf("stdout not JSONL: %q", out.String())
						}
					}
				} else if out.String() != "answer\n" {
					t.Fatalf("stdout=%q", out.String())
				}
			})
		}
	}
}
