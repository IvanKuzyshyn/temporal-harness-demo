package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestJournalCompletedStreamDoesNotResurrect(t *testing.T) {
	dir := t.TempDir()
	records := durableRecords(64)
	records[0].Message = llm.Message{Role: "assistant", Content: "OLD_COMPLETED_SENTINEL"}
	saveHistory(t, dir, "s", records)
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	ref := activities.TranscriptRef("s") + "#" + records[0].ID
	m.applyEvent(retryEvent(1, events.MessageUpdate, "s", "", ref, records[0].Message.Content))
	m.Update(m.syncHistory(&workflows.SessionInfo{DataDir: dir}, m.queryID)())
	if strings.Count(m.viewport.GetContent(), "OLD_COMPLETED_SENTINEL") != 1 {
		t.Fatal("initial reconciliation failed")
	}
	records = append(records, durableRecords(65)[64])
	saveHistory(t, dir, "s", records)
	m.Update(m.loadHistory(0)())
	if m.history.page.Before != 1 || strings.Contains(m.viewport.GetContent(), "OLD_COMPLETED_SENTINEL") {
		t.Fatal("completed record resurrected after page eviction")
	}
	m.Update(m.loadHistory(m.history.page.Before)())
	m.Update(m.loadHistory(0)())
	// No successful Query has cleared the stream. Retry/reconnect replay must
	// not undo reconciliation, even after older -> latest navigation.
	m.applyEvent(retryEvent(2, events.MessageStart, "s", "", ref, ""))
	m.applyEvent(retryEvent(3, events.MessageUpdate, "s", "", ref, "OLD_COMPLETED_SENTINEL"))
	m.refreshViewport(false)
	if strings.Contains(m.viewport.GetContent(), "OLD_COMPLETED_SENTINEL") {
		t.Fatal("replayed completed reference resurrected")
	}
	m.applyEvent(retryEvent(4, events.MessageUpdate, "s", "", "distinct", "OLD_COMPLETED_SENTINEL"))
	m.refreshViewport(false)
	if strings.Count(m.viewport.GetContent(), "OLD_COMPLETED_SENTINEL") != 1 {
		t.Fatal("distinct current message suppressed by text")
	}
	// A new authoritative directory and a new session both reset retirement.
	other := t.TempDir()
	saveHistory(t, other, "s", nil)
	m.Update(m.syncHistory(&workflows.SessionInfo{DataDir: other}, m.queryID)())
	if len(m.stream) != 0 {
		t.Fatal("old-directory live entries retained")
	}
	m.applyEvent(retryEvent(5, events.MessageUpdate, "s", "", ref, "new root"))
	m.refreshViewport(false)
	if !strings.Contains(m.viewport.GetContent(), "new root") {
		t.Fatal("retirement leaked across directories")
	}
	m.switchSession("other")
	m.applyEvent(retryEvent(1, events.MessageUpdate, "other", "", ref, "new session"))
	m.refreshViewport(false)
	if !strings.Contains(m.viewport.GetContent(), "new session") {
		t.Fatal("retirement leaked across sessions")
	}
}

func TestJournalOpaqueWorkflowIdentity(t *testing.T) {
	id := "session\u200dname"
	dir := t.TempDir()
	if _, err := events.NewStore(dir).Path(id); err != nil {
		t.Fatalf("unsupported fixture identity: %v", err)
	}
	records := durableRecords(1)
	records[0].Message = llm.Message{Role: "assistant", Content: "IDENTITY_SENTINEL"}
	saveHistory(t, dir, id, records)
	m := New(nil, Options{SessionID: id})
	defer m.Close()
	historySnapshot(t, m, dir)
	ref := activities.TranscriptRef(id) + "#" + records[0].ID
	m.applyEvent(retryEvent(1, events.MessageUpdate, id, "", ref, "IDENTITY_SENTINEL"))
	m.refreshViewport(false)
	if strings.Count(m.viewport.GetContent(), "IDENTITY_SENTINEL") != 1 {
		t.Fatalf("opaque parent failed reconciliation: %s", m.viewport.GetContent())
	}
	m.applyEvent(retryEvent(2, events.MessageUpdate, id, "", "current", "parent draft"))
	// These raw IDs have exactly the same sanitized display label. Neither
	// other-workflow nor child messages may be suppressed by the parent's ref.
	m.applyEvent(retryEvent(3, events.MessageUpdate, "sessionname", "", ref, "other draft"))
	m.applyEvent(retryEvent(4, events.MessageUpdate, "sessionname", id, ref, "child draft"))
	m.applyEvent(retryEvent(5, events.MessageUpdate, "sessionname", "sessionname", ref, "other parent draft"))
	m.applyEvent(retryEvent(6, events.MessageStart, "sessionname", id, ref, ""))
	m.applyEvent(retryEvent(7, events.MessageUpdate, "sessionname", id, ref, "child replacement"))
	m.refreshViewport(false)
	m.viewport.GotoTop()
	m.toggleWork()
	m.resumeFollowing()
	view := m.viewport.GetContent()
	for _, want := range []string{"parent draft", "child sessionname: Assistant (stream draft, best-effort):", "other draft", "child replacement", "other parent draft"} {
		if !strings.Contains(view, want) {
			t.Fatalf("lost distinct identity %q: %s", want, view)
		}
	}
	if strings.Contains(m.stream[0].label, "child") || len(m.stream) != 4 || strings.Contains(view, "child draft") {
		t.Fatalf("parent/child/retry identities aliased: %+v", m.stream)
	}
	// The same opaque keys must survive the pending Query replay path.
	m.reconnect = nil
	m.beginQuery()
	_, cmd := m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Session: &workflows.SessionInfo{DataDir: dir}}})
	if cmd != nil {
		m.Update(cmd())
	}
	if m.viewport.GetContent() != view {
		t.Fatalf("reconnect lost opaque keys: %s", m.viewport.GetContent())
	}
}

func TestJournalRetirementBoundAndSnapshotLifetime(t *testing.T) {
	m := New(nil, Options{SessionID: "s"})
	defer m.Close()
	for i := 0; i < maxReplayEvents*2; i++ {
		m.retireStream(messageRefIdentity(fmt.Sprint(i)))
	}
	if len(m.history.retired) != maxReplayEvents {
		t.Fatal("retirement cache is not bounded")
	}
	last := m.history.retired[len(m.history.retired)-1]
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID})
	if !m.persistedStream(streamEntry{messageRef: last, workflowKey: optionalIdentity("s")}) {
		t.Fatal("snapshot cleared retirement")
	}
	m.switchSession("other")
	if len(m.history.retired) != 0 {
		t.Fatal("session retained retirement cache")
	}
}

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
	if strings.Contains(m.viewport.GetContent(), "captured output") || !strings.Contains(m.viewport.GetContent(), "▸ Work") {
		t.Fatal("normal overview did not collapse noisy output")
	}
	m.selectedID = "work:" + records[68].ID
	m.toggleWork()
	if !strings.Contains(m.viewport.GetContent(), "display truncated") {
		t.Fatal("expanded overview missing truncation label")
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
	if !strings.Contains(m.viewport.GetContent(), "▾ Work") {
		t.Fatal("work expansion not wired")
	}
	m.Update(key('t', tea.ModCtrl))
	if !strings.Contains(m.viewport.GetContent(), "▸ Work") || strings.Contains(m.viewport.GetContent(), "captured output") {
		t.Fatal("work collapse not wired")
	}
	// No new optimistic conversation row with an unmatchable identity.
	m.prompt.SetValue("optimistic sentinel")
	m.submitPrompt(false)
	if strings.Contains(m.viewport.GetContent(), "optimistic sentinel") {
		t.Fatal("unreconcilable optimistic row")
	}
}
func TestJournalOutcomeSchemasFailClosed(t *testing.T) {
	cases := []struct{ name, content, status string }{
		{"review identity contradiction", `{"toolCallId":"other","tail":"","logRef":"logs/x","exitCode":0,"isError":false}`, "unavailable"},
		{"review execution failure", `{"tail":"","logRef":"logs/x","exitCode":0,"isError":false,"executionError":true}`, "unavailable"},
		{"review partial map", `{"toolCallId":"call","isError":false,"details":{"exitCode":"0","executionError":true}}`, "unavailable"},
		{"review malformed explicit exit", `{"toolCallId":"call","isError":false,"exitCode":"invalid","details":{"exitCode":"0"}}`, "unavailable"},
		{"ordinary zero", `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0","logRef":"logs/x"}}`, "succeeded"},
		{"ordinary malformed explicit exit with full envelope", `{"toolCallId":"call","output":"CAPTURE","isError":false,"exitCode":"invalid","details":{"exitCode":"0"}}`, "unavailable"},
		{"ordinary partial details with full envelope", `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0","executionError":true}}`, "unavailable"},
		{"ordinary denied", `{"toolCallId":"call","output":"CAPTURE","isError":true}`, "failed/denied"},
		{"ordinary nonzero", `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"2"}}`, "failed"},
		{"ordinary failure", `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0","executionError":"true"}}`, "failed"},
		{"ordinary interrupted", `{"toolCallId":"call","output":"CAPTURE","isError":false,"interrupted":true,"details":{"exitCode":"0"}}`, "interrupted"},
		{"ordinary missing exit", `{"toolCallId":"call","output":"CAPTURE","isError":false}`, "unavailable"},
		{"ordinary null exit", `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":null}}`, "unavailable"},
		{"ordinary bad discriminator", `{"toolCallId":"call","output":"CAPTURE","isError":false,"details":{"exitCode":"0","executionError":"invalid"}}`, "unavailable"},
		{"ordinary null discriminator", `{"toolCallId":"call","output":"CAPTURE","isError":false,"interrupted":null,"details":{"exitCode":"0"}}`, "unavailable"},
	}
	verification := `{"tail":"CAPTURE","logRef":"logs/x","exitCode":0,"isError":false,"executionError":false,"interrupted":false,"truncated":false}`
	cases = append(cases, struct{ name, content, status string }{"verification zero", verification, "succeeded"})
	for _, change := range []struct{ name, from, to, status string }{
		{"nonzero", `"exitCode":0`, `"exitCode":2`, "failed"},
		{"null exit", `"exitCode":0`, `"exitCode":null`, "unavailable"},
		{"missing exit", `"exitCode":0,`, ``, "unavailable"},
		{"execution failure", `"executionError":false`, `"executionError":true`, "failed"},
		{"isError failure", `"isError":false`, `"isError":true`, "failed/denied"},
		{"malformed exit", `"exitCode":0`, `"exitCode":"0"`, "unavailable"},
		{"interrupted", `"interrupted":false`, `"interrupted":true`, "interrupted"},
		{"null isError", `"isError":false`, `"isError":null`, "unavailable"},
		{"malformed executionError", `"executionError":false`, `"executionError":"false"`, "unavailable"},
		{"contradictory identity", `"tail":`, `"toolCallId":"other","tail":`, "unavailable"},
		{"null identity", `"tail":`, `"toolCallId":null,"tail":`, "unavailable"},
		{"malformed identity", `"tail":`, `"toolCallId":123,"tail":`, "unavailable"},
		{"empty identity", `"tail":`, `"toolCallId":"","tail":`, "unavailable"},
	} {
		cases = append(cases, struct{ name, content, status string }{"verification " + change.name, strings.Replace(verification, change.from, change.to, 1), change.status})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := durableToolText("run_command", "call", tc.content)
			if !strings.Contains(got, tc.status) || (tc.status != "succeeded" && strings.Contains(got, "[succeeded]")) {
				t.Fatalf("want %s, got %s", tc.status, got)
			}
			for _, capture := range []string{"CAPTURE", "logs/x", "executionError", "exitCode"} {
				if strings.Contains(tc.content, capture) && !strings.Contains(got, capture) {
					t.Fatalf("lost captured %s: %s", capture, got)
				}
			}
		})
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
	m.selectedID = "work:" + records[62].ID
	m.toggleWork()
	m.selectedID = "work:live"
	m.toggleWork()
	m.resumeFollowing()
	text := m.viewport.GetContent()
	// The rendered view now includes trusted outcome SGR. Untrusted journal
	// content must still be control-free before that styling is applied.
	if strings.ContainsAny(boundedJournal(m.journalLines()), "\x1b\x07\u202e") {
		t.Fatal("untrusted terminal controls retained")
	}
	if len(text) > maxJournalText || strings.ContainsAny(text, "\x07\u202e") || !strings.Contains(text, "durable child summary") || !strings.Contains(text, "child-artifact#message") || !strings.Contains(text, "display limit") {
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
	m.selectedID = "work:live"
	m.toggleWork()
	if !strings.Contains(m.viewport.GetContent(), "child draft") {
		t.Fatal("lost expanded child distinction")
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
