package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type approvalBackend struct {
	fakeBackend
	decisions   []workflows.DecisionInput
	decisionCtx context.Context
}

func (f *approvalBackend) Decide(ctx context.Context, _ string, d workflows.DecisionInput) error {
	f.decisionCtx = ctx
	f.decisions = append(f.decisions, d)
	return ctx.Err()
}
func approvalModel(t *testing.T) (*Model, *approvalBackend) {
	t.Helper()
	t.Setenv("USER", "alice")
	f := &approvalBackend{}
	f.state = workflows.State{Status: "blocked", PendingApproval: &workflows.PendingApproval{RequestID: "request", ActionHash: "hash", ExpiresAt: time.Now().Add(time.Minute), Preview: "-old\n+new\x1b[2J", Call: llm.ToolCall{Name: "write_file", Args: []byte(`{"path":"x","content":"new"}`)}}}
	m := New(f, Options{SessionID: "session"})
	t.Cleanup(m.Close)
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state, at: time.Now()})
	return m, f
}

func TestApprovalModalDecisions(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "approve", true: "deny"}[deny], func(t *testing.T) {
			m, f := approvalModel(t)
			view := m.View().Content
			for _, text := range []string{"blocked", "request", "write_file", "-old", "+new", "a approve", "d deny"} {
				if !strings.Contains(view, text) {
					t.Errorf("missing %q: %s", text, view)
				}
			}
			if strings.Contains(m.approvalOverlay(), "\x1b") {
				t.Fatal("unsafe preview")
			}
			var cmd tea.Cmd
			if deny {
				m.Update(key('d', 0))
				_, cmd = m.Update(key(tea.KeyEnter, 0))
				if cmd != nil {
					t.Fatal("empty denial sent")
				}
				m.Update(tea.PasteMsg{Content: "unsafe\x1b\nchange"})
				_, cmd = m.Update(key(tea.KeyEnter, 0))
			} else {
				_, cmd = m.Update(key('a', 0))
			}
			if cmd == nil {
				t.Fatal("no decide command")
			}
			if len(f.decisions) != 0 {
				t.Fatal("synchronous backend call")
			}
			msg := cmd()
			m.Update(msg)
			if len(f.decisions) != 1 {
				t.Fatalf("decisions=%v notice=%s", f.decisions, m.notice)
			}
			d := f.decisions[0]
			if d.RequestID != "request" || d.ActionHash != "hash" || d.User != "alice" || d.Approved == deny {
				t.Fatal(d)
			}
			if deny && d.Reason != "unsafe change" {
				t.Fatalf("reason=%q", d.Reason)
			}
			if _, cmd = m.Update(key('a', 0)); cmd != nil {
				t.Fatal("duplicate local decision sent")
			}
		})
	}
}

func TestApprovalModalStaleAndLifetime(t *testing.T) {
	for _, mode := range []string{"request", "hash", "expired", "switch", "close", "empty user"} {
		t.Run(mode, func(t *testing.T) {
			m, f := approvalModel(t)
			_, cmd := m.Update(key('a', 0))
			if cmd == nil {
				t.Fatal("no command")
			}
			switch mode {
			case "request":
				f.state.PendingApproval.RequestID = "new"
			case "hash":
				f.state.PendingApproval.ActionHash = "new"
			case "expired":
				f.state.PendingApproval.ExpiresAt = time.Now().Add(-time.Second)
			case "switch":
				m.switchSession("other")
			case "close":
				m.Close()
			case "empty user":
				t.Setenv("USER", "")
			}
			msg := cmd()
			m.Update(msg)
			if len(f.decisions) != 0 {
				t.Fatal("stale decision sent")
			}
			if mode == "switch" && m.notice != "Connecting" {
				t.Fatal("stale epoch result changed UI")
			}
		})
	}
}

type blockingApprovalBackend struct {
	approvalBackend
	entered chan context.Context
}

func (f *blockingApprovalBackend) Decide(ctx context.Context, _ string, _ workflows.DecisionInput) error {
	f.entered <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestApprovalModalInflightCancellation(t *testing.T) {
	for _, mode := range []string{"switch", "close", "parent"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("USER", "alice")
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &blockingApprovalBackend{entered: make(chan context.Context, 1)}
			f.state = workflows.State{PendingApproval: &workflows.PendingApproval{RequestID: "request", ActionHash: "hash", ExpiresAt: time.Now().Add(time.Minute)}}
			m := New(f, Options{Context: parent, SessionID: "one"})
			defer m.Close()
			m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: f.state})
			_, cmd := m.Update(key('a', 0))
			done := make(chan tea.Msg, 1)
			go func() { done <- cmd() }()
			var operationCtx context.Context
			select {
			case operationCtx = <-f.entered:
			case <-time.After(time.Second):
				t.Fatal("Decide did not start")
			}
			switch mode {
			case "switch":
				m.switchSession("other")
			case "close":
				m.Close()
			case "parent":
				cancel()
			}
			select {
			case <-operationCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("Decide context leaked")
			}
			select {
			case msg := <-done:
				m.Update(msg)
			case <-time.After(time.Second):
				t.Fatal("Decide did not return")
			}
			if mode == "switch" && m.notice != "Connecting" {
				t.Fatal("old session changed notice")
			}
		})
	}
}

func TestApprovalModalRejectsOldSnapshotResult(t *testing.T) {
	m, f := approvalModel(t)
	_, cmd := m.Update(key('a', 0))
	msg := cmd()
	newer := *f.state.PendingApproval
	newer.RequestID = "new-request"
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{PendingApproval: &newer}})
	m.Update(msg)
	if m.approval.sent || m.approval.requestID != "new-request" {
		t.Fatal("old decision resolved a newer modal")
	}
	m.Update(key('d', 0))
	m.Update(tea.PasteMsg{Content: "old reason"})
	newer.ActionHash = "new-hash"
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{PendingApproval: &newer}})
	if m.approval.reason != "" || m.approval.denying {
		t.Fatal("reason carried to changed action")
	}
}

func TestApprovalModalReasonBoundCountdownAndStop(t *testing.T) {
	m, f := approvalModel(t)
	m.Update(key('d', 0))
	m.Update(tea.PasteMsg{Content: strings.Repeat("界", 10000)})
	if len(m.approval.reason) > 4096 {
		t.Fatal("unbounded reason")
	}
	m.state.PendingApproval.ExpiresAt = time.Now().Add(-time.Second)
	if !strings.Contains(m.approvalView(), "expired") {
		t.Fatal(m.approvalView())
	}
	_, cmd := m.Update(key(tea.KeyEscape, 0))
	if cmd == nil {
		t.Fatal("Esc unavailable")
	}
	cmd()
	if f.stops != 1 {
		t.Fatal("stop missing")
	}
	_, cmd = m.Update(key('c', tea.ModCtrl))
	if cmd == nil || m.ctx.Err() == nil {
		t.Fatal("quit unavailable")
	}
}
