package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type fakeBackend struct {
	state                     workflows.State
	queryErr                  error
	queries, stops, continues int
	inputs                    []workflows.Input
	messages                  []workflows.MessageInput
	ids                       []string
}

func (f *fakeBackend) Submit(_ context.Context, id string, in workflows.Input, msg workflows.MessageInput) (workflows.MessageReceipt, error) {
	f.ids = append(f.ids, id)
	f.inputs = append(f.inputs, in)
	f.messages = append(f.messages, msg)
	return workflows.MessageReceipt{Disposition: "accepted"}, nil
}
func (f *fakeBackend) Query(context.Context, string) (workflows.State, error) {
	f.queries++
	return f.state, f.queryErr
}
func (f *fakeBackend) Stop(context.Context, string) error                            { f.stops++; return nil }
func (f *fakeBackend) Continue(context.Context, string) error                        { f.continues++; return nil }
func (f *fakeBackend) Decide(context.Context, string, workflows.DecisionInput) error { return nil }
func (f *fakeBackend) Sessions(context.Context) ([]string, error)                    { return []string{"one", "two"}, nil }
func testModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	f := &fakeBackend{}
	m := New(f, Options{SessionID: "one", Model: "mock", DataDir: t.TempDir(), Input: workflows.Input{MaxTurns: 8}})
	t.Cleanup(func() { m.cancel() })
	return m, f
}
func key(code rune, mod tea.KeyMod) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: mod} }
func runCommand(t *testing.T, m *Model, text string, mod tea.KeyMod) tea.Msg {
	t.Helper()
	m.prompt.SetValue(text)
	_, cmd := m.Update(key(tea.KeyEnter, mod))
	if cmd == nil {
		t.Fatalf("no command for %q", text)
	}
	return cmd()
}
func delta(seq uint64, text string) streamEvent {
	b, _ := json.Marshal(events.MessageData{Role: "assistant", Delta: text})
	return compactEvent(events.Event{Seq: seq, WorkflowID: "one", Type: events.MessageUpdate, Data: b})
}

func TestTailStartsBeforeSnapshotAndFiltersStale(t *testing.T) {
	m, f := testModel(t)
	store := events.NewStore(m.opts.DataDir)
	for _, s := range []string{"stale", "fresh"} {
		if _, err := store.Append(events.Scope{StreamID: "one", WorkflowID: "one"}, events.MessageUpdate, events.None, events.MessageData{Delta: s}); err != nil {
			t.Fatal(err)
		}
	}
	f.state = workflows.State{LastSeq: 1, Answer: "snapshot", Status: "running"}
	// No backend call occurs before the first tail result is processed.
	msg := m.tailCmd()()
	if f.queries != 0 {
		t.Fatal("query started before tail")
	}
	m.Update(msg)
	if m.reconnect == nil {
		t.Fatal("events must be buffered while snapshot is pending")
	}
	m.Update(m.queryCmd()())
	view := m.View().Content
	if strings.Contains(view, "stale") || !strings.Contains(view, "fresh") || !strings.Contains(view, "snapshot") {
		t.Fatalf("incorrect reconnect view: %s", view)
	}
	if m.lastSeq != 2 || m.offset == 0 {
		t.Fatalf("seq=%d offset=%d", m.lastSeq, m.offset)
	}
}
func TestGapForcesQuery(t *testing.T) {
	m, f := testModel(t)
	m.started = true
	m.reconnect = nil
	m.lastSeq = 4
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(6, "gap")}, offset: 50, at: time.Now()})
	if m.reconnect == nil || !m.queryPending {
		t.Fatal("gap did not initiate snapshot")
	}
	f.state = workflows.State{LastSeq: 6, Answer: "recovered", Status: "idle"}
	m.Update(m.queryCmd()())
	if f.queries != 1 || strings.Contains(m.View().Content, "gap") || !strings.Contains(m.View().Content, "recovered") {
		t.Fatal("gap recovery failed")
	}
}
func TestSendSteerStopContinueAndMultiline(t *testing.T) {
	m, f := testModel(t)
	for _, tc := range []struct {
		text, mode string
		mod        tea.KeyMod
	}{{"hello", "followUp", 0}, {"redirect", "steer", tea.ModAlt}, {"/steer change course", "steer", 0}, {"/explore internal", "followUp", 0}, {"/run go test ./...", "followUp", 0}} {
		msg := runCommand(t, m, tc.text, tc.mod)
		m.Update(msg)
		got := f.messages[len(f.messages)-1]
		if got.WhenBusy != tc.mode || got.RequestID == "" {
			t.Fatalf("bad submission: %+v", got)
		}
	}
	if f.messages[0].RequestID == f.messages[1].RequestID {
		t.Fatal("reused request ID")
	}
	m.prompt.SetValue("line one")
	m.Update(key(tea.KeyEnter, tea.ModShift))
	m.prompt.InsertString("line two")
	if m.prompt.Value() != "line one\nline two" {
		t.Fatalf("multiline lost: %q", m.prompt.Value())
	}
	m.prompt.SetValue(" \n ")
	_, cmd := m.Update(key(tea.KeyEnter, 0))
	if cmd != nil {
		t.Fatal("empty submission")
	}
	_, cmd = m.Update(key(tea.KeyEscape, 0))
	if f.stops != 0 {
		t.Fatal("blocking Update")
	}
	m.Update(cmd())
	if f.stops != 1 {
		t.Fatal("stop not sent")
	}
	m.Update(runCommand(t, m, "/continue", 0))
	if f.continues != 1 {
		t.Fatal("continue not sent")
	}
}
func TestDemoAndSessionCommands(t *testing.T) {
	m, f := testModel(t)
	m.Update(runCommand(t, m, "/demo", 0))
	if f.inputs[0].Scenario != "demo" || f.messages[0].Text == "" {
		t.Fatal("demo not configured")
	}
	m.Update(runCommand(t, m, "/sessions", 0))
	if !strings.Contains(strings.Join(m.commands, "\n"), "two") {
		t.Fatal("sessions not listed")
	}
	old := m.epoch
	m.Update(runCommand(t, m, "/new", 0))
	if m.epoch == old || m.opts.SessionID == "one" || m.offset != 0 {
		t.Fatal("new session not reset")
	}
}
func TestSwitchIgnoresStaleInflightResults(t *testing.T) {
	m, _ := testModel(t)
	old := m.epoch
	m.offset = 42
	m.switchSession("two")
	m.Update(snapshotMsg{epoch: old, query: m.queryID, state: workflows.State{Answer: "wrong", LastSeq: 999}})
	m.Update(tailMsg{epoch: old, events: []streamEvent{delta(1, "wrong")}, offset: 999})
	m.Update(operationMsg{epoch: old, label: "wrong", err: errors.New("wrong")})
	m.Update(newSessionMsg{epoch: old, id: "wrong"})
	if m.lastSeq != 0 || m.offset != 0 || strings.Contains(m.View().Content, "wrong") || m.opts.SessionID != "two" {
		t.Fatal("stale result accepted")
	}
	m.switchSession("one")
	if m.offset != 0 {
		t.Fatal("restored offset is not replay-safe")
	}
}
func TestSnapshotAuthoritativeWithoutStream(t *testing.T) {
	m, f := testModel(t)
	m.started = true
	f.state = workflows.State{Status: "completed", Answer: "final authoritative", LastSeq: 20, Turn: 3, MaxTurns: 8}
	m.Update(m.queryCmd()())
	view := m.View().Content
	if !strings.Contains(view, "final authoritative") || !strings.Contains(view, "completed") || !strings.Contains(view, "3/8") {
		t.Fatalf("missing snapshot: %s", view)
	}
}
func TestRenderingBoundsAndSanitization(t *testing.T) {
	m, _ := testModel(t)
	m.started = true
	m.reconnect = nil
	m.applyEvent(delta(1, "hello\x1b[2J\x07\rworld\u009b31m\u202eevil"))
	m.commands = append(m.commands, safeText("\x1b]52;c;evil\x07\x00"))
	for _, size := range [][2]int{{0, 0}, {1, 1}, {2, 4}, {10, 5}, {80, 24}, {200, 60}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v := m.View().Content
		if lipgloss.Width(v) > size[0] || lipgloss.Height(v) > max(1, size[1]) {
			t.Fatalf("overflow at %v: %dx%d", size, lipgloss.Width(v), lipgloss.Height(v))
		}
		if strings.Contains(v, "\x1b[2J") || strings.ContainsAny(v, "\x07\r\u009b\u202e") {
			t.Fatalf("unsafe terminal output %q", v)
		}
	}
}
func TestQuitCancelsAndStopsUpdates(t *testing.T) {
	m, _ := testModel(t)
	_, cmd := m.Update(key('c', tea.ModCtrl))
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not quit")
	}
	if m.ctx.Err() == nil {
		t.Fatal("context not canceled")
	}
	_, cmd = m.Update(tailMsg{epoch: m.epoch})
	if cmd != nil {
		t.Fatal("scheduled work after quit")
	}
}
