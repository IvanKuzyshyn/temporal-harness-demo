package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

func TestReadingWindowSurvivesTailEvictionAndResize(t *testing.T) {
	m, _ := testModel(t)
	dir := t.TempDir()
	records := durableRecords(64)
	for i := range records {
		records[i].Message.Content = strings.Repeat("evidence line\n", 4)
	}
	saveHistory(t, dir, m.opts.SessionID, records)
	historySnapshot(t, m, dir)
	m.prompt.SetValue("draft untouched")
	m.Update(key(tea.KeyPgUp, 0))
	before := m.viewport.View()
	id := m.history.page.Entries[0].ID
	saveHistory(t, dir, m.opts.SessionID, durableRecords(140))
	historySnapshot(t, m, dir)
	if m.viewport.View() != before || m.history.page.Entries[0].ID != id {
		t.Fatal("new tail evicted reading anchor")
	}
	if m.unread != 76 {
		t.Fatalf("unread=%d", m.unread)
	}
	historySnapshot(t, m, dir)
	if m.unread != 76 {
		t.Fatal("same snapshot double-counted")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.anchorID == "" {
		t.Fatal("no durable record anchor")
	}
	_, cmd := m.Update(key(tea.KeyEnd, 0))
	if cmd == nil {
		t.Fatal("no latest read")
	}
	m.Update(cmd())
	if m.history.page.Before != 76 || !m.followLive || m.unread != 0 {
		t.Fatal("latest did not replace frozen window")
	}
	if m.prompt.Value() != "draft untouched" {
		t.Fatal("draft lost")
	}
}
func TestOlderWindowStillReportsNewDurableRecords(t *testing.T) {
	m, _ := testModel(t)
	dir := t.TempDir()
	saveHistory(t, dir, m.opts.SessionID, durableRecords(140))
	historySnapshot(t, m, dir)
	m.Update(m.loadHistory(m.history.page.Before)())
	before := m.history.page.Before
	saveHistory(t, dir, m.opts.SessionID, durableRecords(145))
	historySnapshot(t, m, dir)
	if m.unread != 5 || m.history.page.Before != before {
		t.Fatal("older window lost notification or anchor")
	}
}

func TestCollapsedOutcomeRetainsMeaning(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = durableRecords(1)
	m.collapseTools = true
	m.history.page.Entries[0].Message = llm.Message{Role: "tool", ToolName: "run_command", ToolCallID: "c", Content: `{"toolCallId":"c","isError":false,"output":"","details":{"exitCode":"2"}}`}
	m.refreshViewport(true)
	if !strings.Contains(m.viewport.GetContent(), "[failed]") {
		t.Fatal("collapse hid captured failure")
	}
}

func TestEndSupersedesAndCancelsPendingOlderRead(t *testing.T) {
	m, _ := testModel(t)
	dir := t.TempDir()
	saveHistory(t, dir, m.opts.SessionID, durableRecords(140))
	historySnapshot(t, m, dir)
	older := m.loadHistory(m.history.page.Before)
	_, latest := m.Update(key(tea.KeyEnd, 0))
	stale := older().(historyMsg)
	if stale.err == nil {
		t.Fatal("superseded read still performed filesystem work")
	}
	m.Update(stale)
	m.Update(latest())
	if m.history.older || !m.followLive || m.history.page.Before != 76 {
		t.Fatal("End swallowed by pending older request")
	}
}

func TestHelpAndMetadataAreReachableFromDetail(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = durableRecords(1)
	m.selectedID = m.history.page.Entries[0].ID
	m.toggleDetail()
	m.prompt.SetValue("/help")
	m.Update(key(tea.KeyEnter, 0))
	if !strings.Contains(m.viewport.GetContent(), "/lognext") || m.history.detailID != "" {
		t.Fatal("help trapped behind detail")
	}
	m.toggleDetail()
	m.prompt.SetValue("/metadata")
	m.Update(key(tea.KeyEnter, 0))
	if !strings.Contains(m.viewport.GetContent(), "Session: one") || m.history.detailID != "" {
		t.Fatal("metadata trapped behind detail")
	}
	m.showPatterns = true
	m.Update(key(tea.KeyEnd, 0))
	if m.showPatterns {
		t.Fatal("End did not restore live journal")
	}
}

func TestRecordSelectionDetailsAndTyping(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = durableRecords(3)
	m.history.loaded = true
	m.history.page.Entries[1].Message = llm.Message{Role: "tool", ToolName: "read_file", Content: strings.Repeat("line\n", 100) + "DETAIL-END"}
	m.refreshViewport(true)
	m.prompt.SetValue("draft")
	m.Update(key(tea.KeyUp, tea.ModCtrl))
	m.Update(key(tea.KeyUp, tea.ModCtrl))
	m.Update(key('o', tea.ModCtrl))
	if m.history.detailID != m.history.page.Entries[1].ID || !strings.Contains(m.viewport.GetContent(), "DETAIL-END") {
		t.Fatal("selected detail not expanded")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.prompt.Value() != "draftx" {
		t.Fatal("typing consumed by journal")
	}
	m.Update(key(tea.KeyPgDown, 0))
	if m.viewport.YOffset() == 0 {
		t.Fatal("detail not scrollable")
	}
	m.Update(key('o', tea.ModCtrl))
	if m.history.detailID != "" || m.prompt.Value() != "draftx" {
		t.Fatal("collapse lost draft")
	}
}
