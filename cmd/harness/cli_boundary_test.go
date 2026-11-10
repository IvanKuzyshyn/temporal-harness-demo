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
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestAutomaticWorkSettlement(t *testing.T) {
	for _, status := range []string{"idle", "failed", "settled"} {
		for _, counter := range []int{4, 5} {
			for _, continuation := range []bool{false, true} {
				s := workflows.State{Status: status, RunCounter: counter, LastRunFailure: &workflows.Failure{Reason: "model_error"}}
				if continuation {
					s.ContinueRequested = true
				} else {
					s.Inbox = []workflows.MessageInput{{RequestID: "retained"}}
				}
				baseline := workflows.State{Status: "working", RunCounter: 4}
				for _, paused := range []bool{true, false} {
					s.QueuePaused = paused
					if sessionSettled(s) != paused || runSettled(s, baseline) != paused {
						t.Fatalf("incorrect quiescence: %+v", s)
					}
					states := []workflows.State{s}
					if !paused {
						states = append(states, workflows.State{Status: "working", RunCounter: 6}, workflows.State{Status: "idle", RunCounter: 6})
					}
					f := &cliFake{states: states}
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					got, err := pollSession(ctx, f, "s", "settled", time.Millisecond)
					cancel()
					if err != nil || f.queries != len(states) {
						t.Fatalf("paused=%v continue=%v queries=%d: %v", paused, continuation, f.queries, err)
					}
					if (settlementError(got) != nil) != paused {
						t.Fatalf("wrong failure: %+v", got)
					}
				}
				s.QueuePaused = true
				if runSettled(s, s) {
					t.Fatal("unchanged baseline failure accepted")
				}
				if runSettled(s, workflows.State{Status: "working", RunCounter: counter + 1}) {
					t.Fatal("older counter accepted")
				}
			}
		}
	}
}

func TestTerminalOutputBoundary(t *testing.T) {
	// Include OSC clipboard/title, CSI, all C0 except permitted whitespace,
	// DEL and C1 introducers. Split ESC from the remaining streamed sequence.
	payload := "hello\n\t世界\x1b]52;c;Y2xpcA==\a\x1b[2J\u009b2J\u009dtitle\u009c"
	for r := rune(0); r < 32; r++ {
		if r != '\n' && r != '\t' {
			payload += string(r)
		}
	}
	payload += "\x7f"
	for _, asJSON := range []bool{false, true} {
		dir := t.TempDir()
		f := &cliFake{states: []workflows.State{{Status: "idle", RunCounter: 1}, {Status: "idle", RunCounter: 2, Answer: payload}}}
		f.submit = func() {
			for _, delta := range []string{"\x1b", payload} {
				_, err := events.NewStore(dir).Append(events.Scope{StreamID: "s", WorkflowID: "s"}, events.MessageUpdate, events.None, events.MessageData{Delta: delta})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		var out, diag bytes.Buffer
		if err := runSession(context.Background(), f, config.Config{DataDir: dir}, runOptions{session: "s", prompt: "new", json: asJSON, timeout: time.Second}, &out, &diag, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 3 {
				t.Fatalf("invalid JSONL: %q", out.String())
			}
			for _, line := range lines {
				if !json.Valid([]byte(line)) {
					t.Fatalf("invalid JSON: %q", line)
				}
			}
			var final cliSummary
			if err := json.Unmarshal([]byte(lines[2]), &final); err != nil || final.Answer != payload {
				t.Fatalf("JSON payload changed: %+v %v", final, err)
			}
			if strings.ContainsAny(out.String(), "\x1b\x00\x07\r") {
				t.Fatal("JSON contains unescaped C0")
			}
		} else {
			for _, r := range out.String() {
				if unicode.IsControl(r) && r != '\n' && r != '\t' {
					t.Fatalf("terminal control %U leaked", r)
				}
			}
			if !strings.Contains(out.String(), "hello\n\t世界") {
				t.Fatal("ordinary whitespace/text lost")
			}
			if out.String() != terminalText(payload)+"\n"+terminalText(payload)+"\n" {
				t.Fatalf("unexpected plain output: %q", out.String())
			}
		}
	}
	failure := workflows.State{LastRunFailure: &workflows.Failure{Reason: payload, Detail: payload}}
	if err := settlementError(failure); strings.ContainsAny(err.Error(), "\x1b\a\r") {
		t.Fatalf("failure controls leaked: %q", err)
	}
}
