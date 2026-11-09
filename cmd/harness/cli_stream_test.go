package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/config"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestInitialStatENOTDIRDisablesTailButSubmits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	store := events.NewStore(dir)
	path, err := store.Path("s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("fixture must fail Stat with ENOTDIR: %v", err)
	}
	f := &cliFake{states: []workflows.State{{Status: "idle", RunCounter: 4}, {Status: "working", RunCounter: 5}, {Status: "idle", RunCounter: 5, Answer: "authoritative"}}}
	f.submit = func() {
		// Even if local storage recovers, do not tail from zero and replay history.
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.AgentSettled, events.None, events.AgentData{Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	var out, diag bytes.Buffer
	err = runSession(context.Background(), f, config.Config{DataDir: dir}, runOptions{session: "s", prompt: "new", json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var final cliSummary
	if err := json.Unmarshal(out.Bytes(), &final); err != nil || final.Answer != "authoritative" || final.Type != "session_state" {
		t.Fatalf("stdout must contain only final state: %q (%v)", out.String(), err)
	}
	if f.submitted.RequestID == "" || f.queries != 3 || strings.Count(diag.String(), "warning:") != 1 || !strings.Contains(diag.String(), "recovering via state query") {
		t.Fatalf("submit=%+v queries=%d stderr=%q", f.submitted, f.queries, diag.String())
	}
}

func TestInvalidSessionIDStillFatal(t *testing.T) {
	f := &cliFake{states: []workflows.State{{Status: "idle"}}}
	var out, diag bytes.Buffer
	err := runSession(context.Background(), f, config.Config{DataDir: t.TempDir()}, runOptions{session: "../invalid", prompt: "new", json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "invalid stream ID") || f.submitted.RequestID != "" || out.Len() != 0 || diag.Len() != 0 {
		t.Fatalf("err=%v submit=%+v stdout=%q stderr=%q", err, f.submitted, out.String(), diag.String())
	}
}

func TestCLITailBoundAndRecoverySemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	line := "{\"seq\":1,\"data\":{}}\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, cliEventBatch+1)+"partial"), 0600); err != nil {
		t.Fatal(err)
	}
	batch, offset, err := tailCLIEvents(path, 0)
	if err != nil || len(batch) != 64 || offset != int64(64*len(line)) {
		t.Fatalf("batch=%d offset=%d err=%v", len(batch), offset, err)
	}
	batch, next, err := tailCLIEvents(path, offset)
	if err != nil || len(batch) != 1 || next != offset+int64(len(line)) {
		t.Fatalf("batch=%d offset=%d err=%v", len(batch), next, err)
	}
	for _, bad := range []string{"broken\n", "{\"seq\":0,\"data\":{}}\n", "{\"seq\":1}\n", "{\"seq\":1,\"data\":\"\xff\"}\n", strings.Repeat("x", events.MaxRecordBytes)} {
		if err := os.WriteFile(path, []byte(line+bad), 0600); err != nil {
			t.Fatal(err)
		}
		batch, next, err := tailCLIEvents(path, 0)
		if err == nil || len(batch) != 1 || next != int64(len(line)) {
			t.Fatalf("invalid record advanced cursor: batch=%d next=%d err=%v", len(batch), next, err)
		}
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tailCLIEvents(path, 1); !errors.Is(err, events.ErrTruncated) {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if batch, next, err := tailCLIEvents(path, 12); len(batch) != 0 || next != 12 || err != nil {
		t.Fatalf("missing file: batch=%d next=%d err=%v", len(batch), next, err)
	}
}

type cliErrorWriter struct{ err error }

func (w cliErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRunOutputWriterError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, asJSON := range []bool{false, true} {
			dir := t.TempDir()
			f := &cliFake{states: []workflows.State{{Status: "idle"}, {Status: "idle", RunCounter: 1, Answer: "final"}}}
			if stream {
				f.submit = func() {
					if _, err := events.NewStore(dir).Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.MessageUpdate, events.None, events.MessageData{Delta: "delta"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			boom := errors.New("output closed")
			err := runSession(context.Background(), f, config.Config{DataDir: dir}, runOptions{session: "s", prompt: "new", json: asJSON, timeout: time.Second}, cliErrorWriter{boom}, io.Discard, time.Millisecond)
			if !errors.Is(err, boom) {
				t.Fatalf("stream=%v json=%v: %v", stream, asJSON, err)
			}
		}
	}
}

func TestStaleApprovalErrorIsNotSuccess(t *testing.T) {
	stale := errors.New("stale approval: action hash changed")
	f := &cliFake{states: []workflows.State{{Status: "idle"}, {Status: "blocked", RunCounter: 1, PendingApproval: &workflows.PendingApproval{RequestID: "current", ActionHash: "queried-hash"}}}, decideError: stale}
	var out, diag bytes.Buffer
	err := runSession(context.Background(), f, config.Config{DataDir: t.TempDir()}, runOptions{session: "s", prompt: "new", autoApprove: true, json: true, timeout: time.Second}, &out, &diag, time.Millisecond)
	if !errors.Is(err, stale) || len(f.decisions) != 1 || f.decisions[0].ActionHash != "queried-hash" || out.Len() != 0 {
		t.Fatalf("err=%v decisions=%+v stdout=%q", err, f.decisions, out.String())
	}
}
