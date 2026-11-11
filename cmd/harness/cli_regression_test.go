package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/api/serviceerror"
)

func TestFastRunJSONAndDemo(t *testing.T) {
	dir := t.TempDir()
	f := &cliFake{queryErrors: map[int]error{0: serviceerror.NewNotFound("new")}, states: []workflows.State{{}, {Status: "idle", RunCounter: 1, Answer: "final\nanswer"}}}
	f.submit = func() {
		_, err := events.NewStore(dir).Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.AgentSettled, events.None, events.AgentData{Status: "done"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var out, diag bytes.Buffer
	err := runSession(context.Background(), f, config.Config{DataDir: dir, Model: "mock"}, runOptions{session: "s", prompt: "Find what's broken in the sample project and verify the fix", json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(out.Bytes(), []byte("\n")), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("lost fast event: %q", out.String())
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("not JSONL: %s", line)
		}
	}
	if f.input.Scenario != "demo" || diag.Len() != 0 {
		t.Fatalf("input=%+v diagnostics=%q", f.input, diag.String())
	}
}
func TestAutoApprovalExplicitOnly(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[auto], func(t *testing.T) {
			pending := &workflows.PendingApproval{RequestID: "current", ActionHash: "hash"}
			f := &cliFake{states: []workflows.State{{Status: "idle"}, {Status: "working", RunCounter: 1, PendingApproval: pending}, {Status: "idle", RunCounter: 1}}}
			var out, diag bytes.Buffer
			err := runSession(context.Background(), f, config.Config{DataDir: t.TempDir()}, runOptions{session: "s", prompt: "hello", autoApprove: auto, json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if auto {
				if len(f.decisions) != 1 || f.decisions[0].RequestID != "current" || f.decisions[0].ActionHash != "hash" || f.decisions[0].User == "" || !strings.Contains(diag.String(), "WARNING") {
					t.Fatalf("decisions=%+v diagnostics=%s", f.decisions, diag.String())
				}
			} else if len(f.decisions) != 0 || diag.Len() != 0 {
				t.Fatal("automatic approval enabled by default")
			}
			if !json.Valid(bytes.TrimSpace(out.Bytes())) {
				t.Fatal(out.String())
			}
		})
	}
}
func TestStreamCorruptionRecoversFromState(t *testing.T) {
	dir := t.TempDir()
	path, err := events.NewStore(dir).Path("s")
	if err != nil {
		t.Fatal(err)
	}
	f := &cliFake{states: []workflows.State{{Status: "idle"}, {Status: "idle", RunCounter: 1, Answer: "recovered"}}}
	f.submit = func() {
		if _, err := events.NewStore(dir).Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.AgentStart, events.None, events.AgentData{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("broken\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, diag bytes.Buffer
	if err := runSession(context.Background(), f, config.Config{DataDir: dir}, runOptions{session: "s", prompt: "hello", timeout: time.Second}, &out, &diag, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if out.String() != "recovered\n" || !strings.Contains(diag.String(), "recovering via state query") {
		t.Fatalf("%q %q", out.String(), diag.String())
	}
}
func TestRunPendingAndTimeout(t *testing.T) {
	f := &cliFake{states: []workflows.State{{Status: "idle", RunCounter: 4}, {Status: "idle", RunCounter: 4}}}
	var out, diag bytes.Buffer
	err := runSession(context.Background(), f, config.Config{DataDir: t.TempDir()}, runOptions{session: "s", prompt: "hello", timeout: 5 * time.Millisecond}, &out, &diag, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if sessionSettled(workflows.State{Status: "idle", RunCounter: 1, Inbox: []workflows.MessageInput{{RequestID: "queued"}}}) {
		t.Fatal("queued run treated as settled")
	}
}
func TestListStatusesAndErrors(t *testing.T) {
	for _, status := range []string{"idle", "working", "failed"} {
		f := &cliFake{states: []workflows.State{{Status: status, Turn: 2}}}
		var out bytes.Buffer
		if err := listSessions(context.Background(), f, &out, true); err != nil {
			t.Fatal(err)
		}
		var item cliSummary
		if err := json.Unmarshal(out.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.ID != "s" || item.Status != status || item.Turn != 2 {
			t.Fatal(item)
		}
	}
	for _, closed := range []error{serviceerror.NewNotFound("gone"), serviceerror.NewFailedPrecondition("closed")} {
		f := &cliFake{queryErrors: map[int]error{0: closed}}
		var out bytes.Buffer
		if err := listSessions(context.Background(), f, &out, true); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "closed/unavailable") {
			t.Fatal(out.String())
		}
	}
	boom := errors.New("offline")
	for _, f := range []*cliFake{{listError: boom}, {queryErrors: map[int]error{0: boom}}} {
		if err := listSessions(context.Background(), f, &bytes.Buffer{}, true); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	}
}
func TestCLIValidationWithoutServices(t *testing.T) {
	for _, tc := range []struct {
		command func([]string, io.Writer) error
		args    []string
	}{
		{runCommand, []string{}}, {runCommand, []string{"-p", "x", "--timeout", "0s"}}, {runCommand, []string{"-p", "x", "extra"}},
		{waitCommand, []string{"s", "--until", "invalid"}}, {waitCommand, []string{"s", "--timeout", "0s"}}, {waitCommand, []string{}},
		{sessionCommand, []string{"list", "extra"}}, {sessionCommand, []string{"nope"}},
	} {
		if err := tc.command(tc.args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %q", tc.args)
		}
	}
}
