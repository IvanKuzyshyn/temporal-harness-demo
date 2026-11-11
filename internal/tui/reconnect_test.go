package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestEventsArrivingDuringQueryAreReplayedOnce(t *testing.T) {
	m, _ := testModel(t)
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(1, "old")}, at: time.Now()})
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(2, " newer")}, offset: 200, at: time.Now()})
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{LastSeq: 1, Answer: "base"}, at: time.Now()})
	if got := m.viewport.GetContent(); strings.Contains(got, "old") || strings.Count(got, "newer") != 1 {
		t.Fatalf("bad replay: %s", got)
	}
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(2, "duplicate")}, at: time.Now()})
	if strings.Contains(m.viewport.GetContent(), "duplicate") {
		t.Fatal("stale live event rendered")
	}
}

func TestPeriodicSnapshotPreservesNewerDeltasWithoutDuplicatingLogs(t *testing.T) {
	m, _ := testModel(t)
	m.started = true
	m.reconnect = nil
	m.lastSnapshot = time.Now()
	tool, _ := json.Marshal(events.ToolData{Name: "run_command", Chunk: "once"})
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(1, "live"), compactEvent(events.Event{Seq: 2, Type: events.ToolUpdate, Data: tool})}, at: time.Now()})
	m.beginQuery()
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{LastSeq: 0, Status: "running"}, at: time.Now()})
	if !strings.Contains(m.viewport.GetContent(), "live") {
		t.Fatal("periodic snapshot erased newer deltas")
	}
	if strings.Count(strings.Join(m.commands, "\n"), "once") != 1 {
		t.Fatal("replayed tool output duplicated")
	}
}

func TestFailedSnapshotRetriesAndStaleQueryIsIgnored(t *testing.T) {
	m, f := testModel(t)
	m.started = true
	f.queryErr = errors.New("offline")
	cmd := m.beginQuery()
	m.Update(cmd())
	if m.queryPending || !strings.Contains(m.notice, "offline") {
		t.Fatal("query error not shown")
	}
	old := m.queryID
	m.Update(tailMsg{epoch: m.epoch, at: m.lastSnapshot.Add(snapshotInterval)})
	if !m.queryPending || m.queryID == old {
		t.Fatal("no retry")
	}
	m.Update(snapshotMsg{epoch: m.epoch, query: old, state: workflows.State{Answer: "wrong"}})
	if m.state.Answer != "" {
		t.Fatal("accepted obsolete query")
	}
	f.queryErr = nil
	f.state = workflows.State{Answer: "back", Status: "idle"}
	m.Update(m.queryCmd()())
	if m.state.Answer != "back" {
		t.Fatal("did not recover")
	}
}

func TestIdlePollingQueriesWithoutStreamEvents(t *testing.T) {
	m, _ := testModel(t)
	m.started = true
	m.reconnect = nil
	m.lastSnapshot = time.Now()
	m.Update(tailMsg{epoch: m.epoch, at: m.lastSnapshot.Add(snapshotInterval)})
	if !m.queryPending {
		t.Fatal("final correctness depends on stream events")
	}
}

func TestTruncatedTailResetsOffsetAndRequeries(t *testing.T) {
	m, _ := testModel(t)
	m.started = true
	m.reconnect = nil
	m.offset = 100
	path, err := events.NewStore(m.opts.DataDir).Path(m.opts.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// Let the store create its directory, then simulate replacement/truncation.
	_, err = events.NewStore(m.opts.DataDir).Append(events.Scope{StreamID: m.opts.SessionID}, events.AgentStart, events.None, events.AgentData{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	m.Update(m.tailCmd()())
	if m.offset != 0 || !m.queryPending {
		t.Fatal("truncation did not reset/requery")
	}
}

func TestBoundedLogsAndSessionCache(t *testing.T) {
	m, _ := testModel(t)
	for i := 0; i < maxEntries+20; i++ {
		m.transcript = appendBounded(m.transcript, strings.Repeat("🙂", maxText))
		m.patterns = appendBounded(m.patterns, strings.Repeat("p", maxText+1))
		m.commands = appendBounded(m.commands, strings.Repeat("c", maxText+1))
	}
	for _, log := range [][]string{m.transcript, m.patterns, m.commands} {
		if len(log) > maxEntries {
			t.Fatal("unbounded log")
		}
		for _, line := range log {
			if len(line) > maxText {
				t.Fatal("unbounded entry")
			}
		}
	}
	for i := 0; i < maxSessions+20; i++ {
		m.switchSession(fmt.Sprintf("s-%d", i))
	}
	if len(m.offsets) > maxSessions || len(m.sessionOrder) > maxSessions {
		t.Fatal("unbounded session cache")
	}
}

func TestApprovalChildToolsAndPaneKeys(t *testing.T) {
	m, _ := testModel(t)
	m.started = true
	m.reconnect = nil
	m.lastSnapshot = time.Now()
	child := delta(1, "child text").Event
	child.WorkflowID = "explorer"
	child.ParentWorkflowID = "one"
	child.Pattern = events.Child
	tool, _ := json.Marshal(events.ToolData{Name: "run_command", Chunk: "output\x1b]52;c;bad\x07", IsError: true})
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{compactEvent(child), compactEvent(events.Event{Seq: 2, Type: events.ToolEnd, Data: tool})}, at: time.Now()})
	if strings.Contains(m.viewport.GetContent(), "child text") {
		t.Fatal("child work leaked while collapsed")
	}
	m.Update(key('t', tea.ModCtrl))
	if !strings.Contains(m.viewport.GetContent(), "child explorer:") {
		t.Fatal("expanded child identity missing")
	}
	m.Update(key(tea.KeyTab, 0))
	if !m.showPatterns || !strings.Contains(m.viewport.GetContent(), "child") {
		t.Fatal("pattern toggle failed")
	}
	m.Update(key(tea.KeyTab, 0))
	m.Update(key('t', tea.ModCtrl))
	if strings.Contains(m.viewport.GetContent(), "child text") {
		t.Fatal("work not collapsed")
	}
	m.state = displayState(workflows.State{PendingApproval: &workflows.PendingApproval{RequestID: "approval-1", Preview: "preview\x1b[2J"}})
	if !strings.Contains(m.approvalView(), "approval-1") || strings.Contains(m.approvalView(), "\x1b") {
		t.Fatal("approval seam unsafe/missing")
	}
}

func TestSessionsAreAvailableInScrollableTranscript(t *testing.T) {
	m, _ := testModel(t)
	m.Update(runCommand(t, m, "/sessions", 0))
	if !strings.Contains(m.viewport.GetContent(), "one") || !strings.Contains(m.viewport.GetContent(), "two") {
		t.Fatal("session list is not scrollable")
	}
}

func TestMultilineApprovalDoesNotHideStatus(t *testing.T) {
	m, _ := testModel(t)
	m.state = displayState(workflows.State{Status: "waiting", PendingApproval: &workflows.PendingApproval{Preview: "line one\nline two\nline three"}})
	view := m.View().Content
	// A blocked session now dedicates the screen to review, not transcript
	// chrome. Its own status and safety/navigation controls must stay visible.
	for _, text := range []string{"Approval blocked", "expired", "PgUp/PgDn", "a approve", "d deny", "Esc stop", "Ctrl+C quit"} {
		if !strings.Contains(view, text) {
			t.Fatalf("multiline approval hid %q: %s", text, view)
		}
	}
}

func TestReconnectOverflowInvalidatesInflightQuery(t *testing.T) {
	m, _ := testModel(t)
	m.started = true
	m.beginQuery()
	old := m.queryID
	batch := make([]streamEvent, events.MaxReconnectEvents+1)
	m.Update(tailMsg{epoch: m.epoch, events: batch, at: time.Now()})
	if m.queryID == old || !m.queryPending {
		t.Fatal("overflow did not restart snapshot")
	}
	m.Update(snapshotMsg{epoch: m.epoch, query: old, state: workflows.State{Answer: "obsolete"}})
	if m.state.Answer != "" {
		t.Fatal("obsolete overflow result accepted")
	}
}

func TestCompactedEventsAndReplayAreBounded(t *testing.T) {
	m, _ := testModel(t)
	for i := 1; i <= maxReplayEvents+20; i++ {
		raw := delta(uint64(i), "").Event
		raw.WorkflowID = strings.Repeat("w", maxText) + fmt.Sprint(i)
		raw.ParentWorkflowID = strings.Repeat("p", maxText) + fmt.Sprint(i)
		raw.Data, _ = json.Marshal(events.MessageData{Delta: strings.Repeat("x", maxText*2)})
		e := compactEvent(raw)
		if len(e.workflowKey) != 64 || len(e.parentKey) != 64 || len(e.WorkflowID) > 256 || len(e.ParentWorkflowID) > 256 {
			t.Fatal("unbounded identity or display labels")
		}
		if len(e.Data) > maxText+100 {
			t.Fatal("oversized compacted event")
		}
		m.applyEvent(e)
	}
	if len(m.recent) > maxReplayEvents || len(m.stream) > maxEntries || len(m.stream[0].text) > maxAnswer {
		t.Fatal("unbounded replay or stream")
	}
}

func TestInitDemoAndEmptyIDAreCommands(t *testing.T) {
	f := &fakeBackend{}
	m := New(f, Options{Demo: true, Autoplay: true, DataDir: t.TempDir()})
	defer m.cancel()
	initial := m.Init()().(tea.BatchMsg)
	if f.queries != 0 || len(f.messages) != 0 {
		t.Fatal("constructor/Init did I/O")
	}
	// First command generates the session ID; its Update only schedules the tail.
	_, tail := m.Update(initial[0]())
	if m.opts.SessionID == "" {
		t.Fatal("no generated ID")
	}
	_, cmd := m.Update(tail())
	batch := cmd().(tea.BatchMsg)
	// Poll timer is first; query and demo submit follow, after the first tail.
	for _, cmd := range batch[1:] {
		m.Update(cmd())
	}
	if len(f.messages) != 1 || f.inputs[0].Scenario != "demo" || f.messages[0].Text != "/demo" {
		t.Fatal("startup demo not submitted")
	}
	// The first authoritative query for the freshly opted-in demo must not
	// conflict with the client's unspecified scenario.
	f.state.Session = &workflows.SessionInfo{Model: m.opts.Model, Workdir: m.opts.Input.Workdir, DataDir: m.opts.Input.DataDir, Scenario: "demo"}
	m.Update(m.queryCmd()())
	if m.configMismatch {
		t.Fatal("fresh autoplay incorrectly reported a configuration mismatch")
	}
}
