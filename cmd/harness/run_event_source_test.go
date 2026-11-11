package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
)

func TestRunEventSourceBeforeSubmit(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		for _, kind := range []string{"original", "raw", "empty", "relative", "invalid", "missing", "file", "legacy", "new"} {
			t.Run(kind+map[bool]string{false: "/plain", true: "/json"}[asJSON], func(t *testing.T) {
				a, b := t.TempDir(), t.TempDir()
				if kind == "raw" {
					a = filepath.Join(a, "raw\tmetadata")
					if err := os.Mkdir(a, 0700); err != nil {
						t.Fatal(err)
					}
				}
				info := &workflows.SessionInfo{DataDir: a}
				switch kind {
				case "empty":
					info.DataDir = ""
				case "relative":
					info.DataDir = "relative"
				case "invalid":
					info.DataDir = a + "\x00"
				case "missing":
					info.DataDir = filepath.Join(a, "absent")
				case "file":
					info.DataDir = filepath.Join(a, "file")
					if err := os.WriteFile(info.DataDir, nil, 0600); err != nil {
						t.Fatal(err)
					}
				case "legacy":
					info = nil
				case "new":
					info.DataDir = b
				}
				appendDelta := func(dir, delta string) {
					t.Helper()
					if _, err := events.NewStore(dir).Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.MessageUpdate, events.None, events.MessageData{Delta: delta}); err != nil {
						t.Fatal(err)
					}
				}
				appendDelta(a, "historical-A")
				appendDelta(b, "historical-B")
				f := &cliFake{states: []workflows.State{{Status: "idle", Session: info}, {Status: "idle", RunCounter: 1, Session: info, Answer: "query-answer"}}}
				if kind == "new" {
					f.queryErrors = map[int]error{0: serviceerror.NewNotFound("new")}
				}
				var out, diag bytes.Buffer
				invalid := kind == "empty" || kind == "relative" || kind == "invalid" || kind == "missing" || kind == "file"
				f.submit = func() {
					if invalid && !strings.Contains(diag.String(), "event stream unavailable") {
						t.Error("missing pre-submit stream warning")
					}
					appendDelta(a, "live-A")
					appendDelta(b, "live-B")
				}
				err := runSession(context.Background(), f, config.Config{DataDir: b}, runOptions{session: "s", prompt: "hello", json: asJSON, timeout: time.Second}, &out, &diag, time.Millisecond)
				if err != nil {
					t.Fatal(err)
				}
				text := out.String()
				wantA := kind == "original" || kind == "raw"
				wantB := kind == "legacy" || kind == "new"
				if strings.Contains(text, "live-A") != wantA || strings.Contains(text, "live-B") != wantB || strings.Contains(text, "historical-") || !strings.Contains(text, "query-answer") {
					t.Fatalf("incorrect stream/final authority: %q", text)
				}
				if kind == "legacy" && (!strings.Contains(diag.String(), "unknown (legacy") || !strings.Contains(diag.String(), "client-selected")) {
					t.Fatalf("undocumented legacy fallback: %q", diag.String())
				}
				if asJSON {
					lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
					for _, line := range lines {
						if !json.Valid(line) {
							t.Fatalf("non-JSON stdout: %q", text)
						}
					}
					var final struct {
						Answer string `json:"answer"`
					}
					if err := json.Unmarshal(lines[len(lines)-1], &final); err != nil {
						t.Fatal(err)
					}
					if final.Answer != "query-answer" {
						t.Fatalf("final not Query-owned: %q", text)
					}
				} else if !strings.HasSuffix(text, "query-answer\n") {
					t.Fatalf("final not Query-owned: %q", text)
				}
			})
		}
	}
}
