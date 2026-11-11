package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func durableRecords(n int) []activities.TranscriptRecord {
	records := make([]activities.TranscriptRecord, n)
	for i := range records {
		records[i] = activities.TranscriptRecord{ID: fmt.Sprintf("%064x", i+1), Message: llm.Message{Role: "user", Content: fmt.Sprintf("message-%d", i)}}
	}
	return records
}
func saveHistory(t *testing.T, dir, id string, records []activities.TranscriptRecord) {
	t.Helper()
	path := filepath.Join(dir, activities.TranscriptRef(id))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func historySnapshot(t *testing.T, m *Model, dir string) {
	t.Helper()
	m.queryID++
	_, cmd := m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Status: "idle", Answer: "final durable answer", Session: &workflows.SessionInfo{DataDir: dir}}, at: time.Now()})
	if cmd == nil {
		t.Fatal("snapshot did not request history")
	}
	m.Update(cmd())
}
func TestDurableReconnectSnapshotWithoutDisplayJSONL(t *testing.T) {
	dir := t.TempDir()
	records := durableRecords(3)
	records[1].Message = llm.Message{Role: "system", Content: "Project context (evidence):\nsentinel"}
	records[2].Message = llm.Message{Role: "assistant", Content: "final durable answer"}
	saveHistory(t, dir, "s", records)
	m := New(nil, Options{SessionID: "s", DataDir: t.TempDir()})
	defer m.Close()
	// No display JSONL exists, and the client-selected directory is wrong.
	m.Update(m.tailCmd()())
	historySnapshot(t, m, dir)
	for i := 0; i < 2; i++ {
		view := m.viewport.GetContent()
		if !strings.Contains(view, "message-0") || strings.Count(view, "final durable answer") != 1 || !strings.Contains(view, "▸ Work") || strings.Contains(view, "sentinel") {
			t.Fatalf("lost/duplicated durable history: %s", view)
		}
		historySnapshot(t, m, dir)
	}
}
func TestHistoryUsesRawAuthoritativeDirectoryAndHonestErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw\nname")
	records := durableRecords(1)
	saveHistory(t, dir, "s", records)
	m := New(nil, Options{SessionID: "s", DataDir: t.TempDir()})
	defer m.Close()
	historySnapshot(t, m, dir)
	if len(m.history.page.Entries) != 1 {
		t.Fatal("sanitized directory used")
	}
	if err := os.WriteFile(filepath.Join(dir, activities.TranscriptRef("s")), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	historySnapshot(t, m, dir)
	if m.state.Status != "idle" || m.history.err == "" || len(m.history.page.Entries) != 1 || !strings.Contains(m.viewport.GetContent(), "History:") {
		t.Fatal("error lost Query status or previous page")
	}
	for _, dir := range []string{"", "relative"} {
		m.switchSession("s")
		m.queryID++
		_, cmd := m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Status: "idle", Answer: "fallback", Session: &workflows.SessionInfo{DataDir: dir}}})
		if cmd != nil {
			m.Update(cmd())
		}
		if m.history.err == "" || !strings.Contains(m.viewport.GetContent(), "fallback") {
			t.Fatal("unknown metadata guessed client path")
		}
	}
}
func TestHistoryPagingStaleResponsesAndPollingDoesNotStarve(t *testing.T) {
	dir := t.TempDir()
	saveHistory(t, dir, "s", durableRecords(140))
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	historySnapshot(t, m, dir)
	cmd := m.loadHistory(m.history.page.Before)
	if cmd == nil {
		t.Fatal("no older command")
	}
	old := cmd()
	// A newer same-directory status Query must not starve an in-flight page.
	m.queryID++
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Session: &workflows.SessionInfo{DataDir: dir}}})
	m.Update(old)
	if !m.history.older || m.history.page.Before != 12 {
		t.Fatal("page starved")
	}
	saveHistory(t, dir, "s", durableRecords(145))
	m.queryID++
	_, cmd = m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Session: &workflows.SessionInfo{DataDir: dir}}})
	if cmd != nil {
		m.Update(cmd())
	}
	if m.history.page.Before != 12 {
		t.Fatal("snapshot displaced older window")
	}
	m.Update(m.loadHistory(0)())
	if m.history.older || m.history.page.Total != 145 || len(m.history.page.Entries) > 64 {
		t.Fatal("latest failed or cache unbounded")
	}
	m.Update(old)
	if m.history.older || m.history.page.Total != 145 {
		t.Fatal("obsolete generation accepted")
	}
	stale := m.loadHistory(0)()
	m.switchSession("other")
	m.Update(stale)
	if len(m.history.page.Entries) != 0 {
		t.Fatal("stale epoch accepted")
	}
}
func TestStreamBecomesDurableWithoutDuplicateFinalAnswer(t *testing.T) {
	dir := t.TempDir()
	records := durableRecords(1)
	records[0].Message = llm.Message{Role: "assistant", Content: "completed answer"}
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	ref := activities.TranscriptRef("s") + "#" + records[0].ID
	m.applyEvent(retryEvent(1, events.MessageStart, "s", "", ref, ""))
	m.applyEvent(retryEvent(2, events.MessageUpdate, "s", "", ref, "completed answer"))
	m.refreshViewport(false)
	if strings.Count(m.viewport.GetContent(), "completed answer") != 1 {
		t.Fatal("stream not visible before persistence")
	}
	saveHistory(t, dir, "s", records)
	// Read while the stream remains present: identity, not a snapshot clearing
	// it or matching answer text, must remove the duplicate projection.
	cmd := m.syncHistory(&workflows.SessionInfo{DataDir: dir}, m.queryID)
	m.Update(cmd())
	if len(m.stream) != 0 || len(m.history.retired) != 1 || strings.Count(m.viewport.GetContent(), "completed answer") != 1 {
		t.Fatal("live to durable transition did not retire the stream exactly once")
	}
	m.queryID++
	_, cmd = m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{LastSeq: 2, Answer: "completed answer", Session: &workflows.SessionInfo{DataDir: dir}}})
	if cmd != nil {
		m.Update(cmd())
	}
	if strings.Count(m.viewport.GetContent(), "completed answer") != 1 {
		t.Fatal("snapshot duplicated or dropped final answer")
	}
}
func TestLivePersistedIdentityAndReusedToolCalls(t *testing.T) {
	dir := t.TempDir()
	records := durableRecords(5)
	records[0].Message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "reused", Name: "run_command", Args: json.RawMessage(`{"command":"one"}`)}}}
	records[1].Message = llm.Message{Role: "tool", ToolCallID: "reused", ToolName: "run_command", Content: `{"output":"first\nsecond\nthird","details":{"logRef":"artifacts/one.log"}}`}
	records[2].Message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "reused", Name: "run_command", Args: json.RawMessage(`{"command":"two"}`)}}}
	records[3].Message = llm.Message{Role: "tool", ToolCallID: "reused", ToolName: "run_command", Content: "other result"}
	records[4].Message = llm.Message{Role: "assistant", Content: "final durable answer"}
	saveHistory(t, dir, "s", records)
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	historySnapshot(t, m, dir)
	ref := activities.TranscriptRef("s") + "#" + records[4].ID
	m.applyEvent(retryEvent(10, events.MessageUpdate, "s", "", ref, "final durable answer"))
	m.refreshViewport(false)
	if strings.Count(m.viewport.GetContent(), "final durable answer") != 1 {
		t.Fatal("live answer duplicated persisted message")
	}
	rows := journalEntries(records)
	if rows[1].parentID != records[0].ID || rows[3].parentID != records[2].ID {
		t.Fatal("tool association crossed logical runs")
	}
	if journalEntries(records[1:])[0].parentID != "" {
		t.Fatal("paired across page boundary")
	}
	m.viewport.GotoTop()
	m.toggleWork()
	if !strings.Contains(m.viewport.GetContent(), "third") || !strings.Contains(m.viewport.GetContent(), "artifacts/one.log") {
		t.Fatal("tool details lost")
	}
	// A stale display ToolEnd cannot rewrite the authoritative result.
	data, _ := json.Marshal(events.ToolData{ToolCallID: "reused", Name: "run_command", IsError: true, Summary: "stale"})
	m.applyEvent(compactEvent(events.Event{Seq: 11, Type: events.ToolEnd, Data: data}))
	if m.history.page.Entries[3].Message.Content != "other result" {
		t.Fatal("live event mutated durable result")
	}
	// Identical content with a different durable ID is a different message.
	m.resumeFollowing()
	records = append(records, activities.TranscriptRecord{ID: fmt.Sprintf("%064x", 99), Message: records[4].Message})
	saveHistory(t, dir, "s", records)
	historySnapshot(t, m, dir)
	if strings.Count(m.viewport.GetContent(), "final durable answer") != 2 {
		t.Fatal("content-based deduplication")
	}
}
