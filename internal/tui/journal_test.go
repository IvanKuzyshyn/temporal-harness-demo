package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestJournalDetailsBoundsAndLatestControls(t *testing.T) {
	dir := t.TempDir()
	records := durableRecords(70)
	output := strings.Repeat("captured output\n", 900) + "END OF CAPTURE"
	payload, _ := json.Marshal(map[string]any{"toolCallId": "call", "output": output, "isError": false, "details": map[string]string{"exitCode": "0", "logRef": "artifacts/command.log"}})
	records[69].Message = llm.Message{Role: "tool", ToolCallID: "call", ToolName: "run_command", Content: string(payload)}
	saveHistory(t, dir, "s", records)
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	historySnapshot(t, m, dir)
	if !strings.Contains(m.viewport.GetContent(), "display truncated") {
		t.Fatal("missing overview truncation label")
	}
	m.prompt.SetValue("/detail 70")
	m.submitPrompt(false)
	view := m.viewport.GetContent()
	for _, want := range []string{"END OF CAPTURE", "artifacts/command.log", "[succeeded]", "origin outside window", "/journal"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail missing %q", want)
		}
	}
	if len(view) > maxJournalText || strings.Contains(view, "\\n") {
		t.Fatal("detail unbounded or captured newlines not scrollable")
	}
	m.Update(key(tea.KeyPgDown, 0))
	if m.viewport.AtTop() {
		t.Fatal("cannot scroll tool detail")
	}
	m.prompt.SetValue("/latest")
	cmd := m.submitPrompt(false)
	if cmd != nil {
		m.Update(cmd())
	}
	if m.history.detailID != "" || !m.viewport.AtBottom() {
		t.Fatal("latest did not leave detail and jump to latest")
	}
	m.prompt.SetValue("/older")
	cmd = m.submitPrompt(false)
	if cmd == nil {
		t.Fatal("older not wired")
	}
	m.Update(cmd())
	if !m.history.older || !m.viewport.AtTop() || m.history.page.Before != 0 {
		t.Fatal("older navigation not anchored")
	}
	before := m.viewport.GetContent()
	m.prompt.SetValue("/older")
	if m.submitPrompt(false) != nil || before != m.viewport.GetContent() {
		t.Fatal("zero cursor incorrectly loaded latest")
	}
	m.prompt.SetValue("/latest")
	m.Update(m.submitPrompt(false)())
	m.Update(key('t', tea.ModCtrl))
	if !strings.Contains(m.viewport.GetContent(), "details collapsed") {
		t.Fatal("tool collapse not wired")
	}
	// No new optimistic conversation row with an unmatchable identity.
	m.prompt.SetValue("optimistic sentinel")
	m.submitPrompt(false)
	if strings.Contains(m.viewport.GetContent(), "optimistic sentinel") {
		t.Fatal("unreconcilable optimistic row")
	}
}
func TestJournalNeverCertifiesOutcomeFromProseOrUnstructuredRead(t *testing.T) {
	cases := []struct{ name, id, content, forbidden string }{
		{"read_file", "call", `{"toolCallId":"call","isError":false,"output":"tests passed"}`, "[succeeded]"},
		{"run_command", "call", `{"toolCallId":"call","isError":false,"output":"tests passed"}`, "[succeeded]"},
		{"run_command", "call", `{"toolCallId":"call","isError":false,"details":{"exitCode":"1"}}`, "[succeeded]"},
		{"run_command", "call", `{"toolCallId":"other","isError":false,"details":{"exitCode":"0"}}`, "[succeeded]"},
		{"run_command", "call", "tests passed", "[succeeded]"},
		{"run_command", "call", `{"tail":"tests passed","logRef":"log","isError":false,"exitCode":"invalid"}`, "[succeeded]"},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if got := durableToolText(tc.name, tc.id, tc.content); strings.Contains(got, tc.forbidden) {
				t.Fatalf("unsupported certification: %s", got)
			}
		})
	}
}
func TestJournalRenderBoundSanitizationAndDurableChildren(t *testing.T) {
	records := durableRecords(64)
	for i := range records {
		records[i].Message.Content = strings.Repeat("x", maxAnswer) + "\x1b\x07\u202e"
	}
	records[63].Message = llm.Message{Role: "tool", ToolName: "explorer", ToolCallID: "child-call", Content: `{"toolCallId":"child-call","isError":false,"output":"durable child summary","details":{"summaryRef":"child-artifact#message"}}`}
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	m.history.loaded = true
	m.history.page.Entries = records
	for i := 0; i < maxEntries+10; i++ {
		m.appendLiveEntry(streamEntry{label: "Live child progress (best-effort): ", text: strings.Repeat("y", maxAnswer)})
	}
	m.refreshViewport(false)
	text := m.viewport.GetContent()
	if len(text) > maxJournalText || strings.ContainsAny(text, "\x1b\x07\u202e") || !strings.Contains(text, "durable child summary") || !strings.Contains(text, "child-artifact#message") || !strings.Contains(text, "display limit") {
		t.Fatal("bounds/sanitization/child summary lost")
	}
	m.history.older = true
	m.refreshViewport(false)
	if strings.Contains(m.viewport.GetContent(), "Live child progress") {
		t.Fatal("live events leaked across older window gap")
	}
	// A persisted parent message does not suppress a child's distinct live message.
	m.history.older = false
	m.stream = nil
	m.applyEvent(retryEvent(1, events.MessageUpdate, "child", "s", "independent", "child draft"))
	m.refreshViewport(false)
	if !strings.Contains(m.viewport.GetContent(), "child draft") {
		t.Fatal("lost child distinction")
	}
}
func TestHistoryErrorVisibleWhileScrolledWithQueryStatus(t *testing.T) {
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	m.history.loaded = true
	m.history.page.Entries = durableRecords(64)
	m.history.err = "artifact unreadable"
	m.state = workflows.State{Status: "idle"}
	m.refreshViewport(true)
	view := m.View().Content
	if !strings.Contains(view, "artifact unreadable") || !strings.Contains(view, "idle") {
		t.Fatal("history error is hidden above scrollback or Query status discarded")
	}
}
func TestHistoryReadIsDeferredAndCanceledOnSessionSwitch(t *testing.T) {
	dir := t.TempDir()
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	m.queryID = 1
	_, cmd := m.Update(snapshotMsg{epoch: m.epoch, query: 1, state: workflows.State{Session: &workflows.SessionInfo{DataDir: dir}}})
	if cmd == nil || !m.history.pending || m.history.loaded || m.history.err != "" {
		t.Fatal("Update performed I/O instead of scheduling it")
	}
	// Create the artifact after Update. Only the command sees it.
	saveHistory(t, dir, "s", durableRecords(2))
	m.Update(cmd())
	if !m.history.loaded || m.history.page.Total != 2 {
		t.Fatal("deferred read failed")
	}
	cmd = m.loadHistory(0)
	m.switchSession("other")
	msg := cmd().(historyMsg)
	if msg.err == nil {
		t.Fatal("session context did not cancel outstanding read")
	}
	m.Update(msg)
	if m.history.loaded || m.history.err != "" {
		t.Fatal("canceled old session modified new session")
	}
}
func TestHistoryMetadataGenerationAndQueryIsolation(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	saveHistory(t, a, "s", durableRecords(2))
	saveHistory(t, b, "s", durableRecords(3))
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	m.queryID = 1
	_, first := m.Update(snapshotMsg{epoch: m.epoch, query: 1, state: workflows.State{Session: &workflows.SessionInfo{DataDir: a}}})
	stale := first().(historyMsg)
	// The right request ID with the wrong originating Query still cannot commit.
	wrong := stale
	wrong.query++
	m.Update(wrong)
	if !m.history.pending || m.history.loaded {
		t.Fatal("wrong query response accepted")
	}
	m.queryID = 2
	_, second := m.Update(snapshotMsg{epoch: m.epoch, query: 2, state: workflows.State{Status: "waiting", Session: &workflows.SessionInfo{DataDir: b}}})
	m.Update(stale)
	if !m.history.pending || m.history.loaded {
		t.Fatal("old directory generation accepted")
	}
	m.Update(second())
	if m.history.page.Total != 3 || m.state.Status != "waiting" {
		t.Fatal("new metadata lost")
	}
	// Duplicate responses are rejected even before another request starts.
	stale = m.loadHistory(0)().(historyMsg)
	m.Update(stale)
	stale.page.Total = 999
	m.Update(stale)
	if m.history.page.Total != 3 {
		t.Fatal("duplicate response committed twice")
	}
}
