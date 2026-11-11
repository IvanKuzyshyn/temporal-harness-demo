package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/charmbracelet/x/ansi"
)

// Assert rendered content, not just retained navigation metadata. The following
// records deliberately keep the viewport far from global bottom clamping.
func TestResizeKeepsActualIntraRecordPosition(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		t.Run(fmt.Sprint(unicode), func(t *testing.T) {
			m, _ := testModel(t)
			records := durableRecords(12)
			var body strings.Builder
			for i := 0; i < 80; i++ {
				if unicode {
					fmt.Fprintf(&body, "%03d界e\u0301%s", i, strings.Repeat("x", 30))
				} else {
					fmt.Fprintf(&body, "%03d%s", i, strings.Repeat("x", 33))
				}
			}
			records[0].Message.Content = body.String()
			for i := 1; i < len(records); i++ {
				records[i].Message.Content = strings.Repeat("FOLLOWING-RECORD\n", 20)
			}
			m.history.page.Entries = records
			m.history.loaded = true
			m.rebuildJournal()
			m.browse()
			m.prompt.SetValue("draft\nkept")
			// One history row, one heading, then 29 rows of 78 body cells.
			// The two-cell inset is presentation, never part of source identity.
			m.viewport.SetYOffset(2 + 29)
			before, frozen := m.viewport.View(), strings.Join(m.journalParts, "\n")
			if !strings.HasPrefix(ansi.Strip(before), "  "+ansi.Cut(body.String(), 2262, 2340)) {
				t.Fatal("fixture did not reach intended content")
			}
			assertAnchoredTop := func() {
				t.Helper()
				top := strings.TrimRight(strings.Split(ansi.Strip(m.viewport.View()), "\n")[0], " ")
				if !strings.HasPrefix(top, "  ") {
					t.Fatal("reflow lost body inset")
				}
				text := top[2:]
				index := strings.Index(body.String(), text)
				if index < 0 {
					t.Fatalf("reflow invented source text: %q", top)
				}
				start := ansi.StringWidth(body.String()[:index])
				if start > 2262 || start+ansi.StringWidth(text) <= 2262 {
					t.Fatalf("visible top escaped original source cell 2262: %q (cell %d)", top, start)
				}
			}
			// Frozen rendered content must not be reconstructed on resize.
			m.history.page.Entries[0].Message.Content = "REPLACED"
			for _, width := range []int{120, 80, 81, 119, 80} {
				m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
				assertAnchoredTop()
				want := strings.TrimRight(strings.Split(ansi.Strip(m.viewport.View()), "\n")[0], " ")
				if view := ansi.Strip(m.View().Content); !strings.Contains(view, want) || !strings.Contains(view, "draft") || !strings.Contains(view, "kept") {
					t.Fatal("anchored content or composer not visible in terminal frame")
				}
				if strings.Join(m.journalParts, "\n") != frozen || m.prompt.Value() != "draft\nkept" {
					t.Fatal("resize changed frozen content or draft")
				}
			}
			if m.viewport.View() != before {
				t.Fatal("round-trip resize drifted")
			}
			m.patterns = []string{"PATTERN-ONLY"}
			for i := 0; i < 20; i++ {
				m.Update(key(tea.KeyTab, 0))
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				m.Update(key(tea.KeyPgDown, 0))
				m.Update(key(tea.KeyTab, 0))
				assertAnchoredTop()
				m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
				if m.viewport.View() != before || len(m.journalParts) != len(records)+1 {
					t.Fatal("repeated panel visits changed retained window/position")
				}
			}
		})
	}
}

func TestPatternsPreserveOlderWindowAcrossReconnect(t *testing.T) {
	m, _ := testModel(t)
	dir := t.TempDir()
	saveHistory(t, dir, m.opts.SessionID, durableRecords(140))
	historySnapshot(t, m, dir)
	m.Update(m.loadHistory(m.history.page.Before)())
	m.Update(key(tea.KeyPgDown, 0))
	m.patterns = []string{"PATTERN-ONLY"}
	m.unread = 3
	m.prompt.SetValue("unsent draft")
	before, offset, page := m.viewport.View(), m.viewport.YOffset(), m.history.page.Before
	m.Update(key(tea.KeyTab, 0))
	if m.followLive || m.unread != 3 {
		t.Errorf("Tab changed follow/unseen policy: follow=%v unseen=%d", m.followLive, m.unread)
	}
	if !strings.Contains(m.View().Content, "PATTERN-ONLY") || !strings.Contains(m.View().Content, "Pattern log") {
		t.Fatal("pattern panel not actually visible")
	}
	saveHistory(t, dir, m.opts.SessionID, durableRecords(145))
	historySnapshot(t, m, dir)
	m.Update(key(tea.KeyTab, 0))
	if m.history.page.Before != page || m.viewport.YOffset() != offset || m.viewport.View() != before || m.followLive || m.unread != 8 {
		t.Fatalf("panel visit lost frozen older state: page=%d offset=%d follow=%v unseen=%d", m.history.page.Before, m.viewport.YOffset(), m.followLive, m.unread)
	}
	if m.prompt.Value() != "unsent draft" {
		t.Fatal("Tab changed draft")
	}
	_, cmd := m.Update(key(tea.KeyEnd, 0))
	m.Update(cmd())
	if !m.followLive || m.unread != 0 || m.history.older || m.showPatterns {
		t.Fatal("End failed to reset")
	}
}

func TestPatternsRestoreDetailAndLogPosition(t *testing.T) {
	for _, panel := range []string{"detail", "log"} {
		t.Run(panel, func(t *testing.T) {
			m, _ := testModel(t)
			m.history.page.Entries = durableRecords(1)
			m.history.page.Entries[0].Message.Content = strings.Repeat("DETAIL-EVIDENCE\n", 80)
			m.selectedID = m.history.page.Entries[0].ID
			m.toggleDetail()
			if panel == "log" {
				m.log = logState{ref: "captured-ref", recordID: m.selectedID}
				m.log.page.Offset = 16384
				m.log.page.Text = strings.Repeat("LOG-EVIDENCE\n", 80)
				m.rebuildJournal()
			}
			m.Update(key(tea.KeyPgDown, 0))
			before, offset := m.viewport.View(), m.viewport.YOffset()
			m.patterns = []string{"PATTERN-ONLY"}
			m.unread = 7
			m.Update(key(tea.KeyTab, 0))
			view := m.View().Content
			if !strings.Contains(view, "Pattern log") || !strings.Contains(view, "PATTERN-ONLY") || strings.Contains(view, "EVIDENCE") {
				t.Errorf("pattern chrome/body disagree: %s", view)
			}
			m.refreshViewport(false)
			m.Update(key(tea.KeyTab, 0))
			if m.viewport.View() != before || m.viewport.YOffset() != offset || m.followLive || m.unread != 7 {
				t.Fatal("Tab lost underlying reading state")
			}
			if panel == "log" && (m.log.page.Offset != 16384 || !strings.Contains(m.View().Content, "Captured command log")) {
				t.Fatal("log page/chrome not restored")
			}
		})
	}
}

func TestPatternsDoNotConsumeHiddenLogReplyOrChangePolicy(t *testing.T) {
	m, _ := testModel(t)
	m.browse()
	m.log = logState{ref: "ref", recordID: "record", generation: 4, pending: true}
	m.log.page.Text = strings.Repeat("OLD\n", 80)
	m.rebuildJournal()
	m.patterns = []string{strings.Repeat("PATTERN\n", 80)}
	m.unread = 6
	m.Update(key(tea.KeyTab, 0))
	m.Update(key(tea.KeyPgDown, 0))
	before, offset := m.viewport.View(), m.viewport.YOffset()
	current := logMsg{epoch: m.epoch, generation: 4, ref: "ref", recordID: "record", page: activities.CommandLogPage{Offset: 16384, Text: "NEW-PAGE"}}
	stale := current
	stale.generation--
	m.Update(stale)
	if !m.log.pending {
		t.Fatal("secondary view bypassed generation guard")
	}
	m.Update(current)
	if m.viewport.View() != before || m.viewport.YOffset() != offset || !m.showPatterns || m.followLive || m.unread != 6 {
		t.Fatal("hidden log response disturbed pattern reading/policy")
	}
	m.Update(key(tea.KeyTab, 0))
	if m.log.page.Offset != 16384 || !strings.Contains(m.viewport.View(), "NEW-PAGE") || m.viewport.YOffset() != 0 {
		t.Fatal("explicit pending log page not ready on return")
	}
	m.Update(key(tea.KeyTab, 0))
	m.switchSession("new-session")
	m.Update(current)
	if m.showPatterns || m.log.ref != "" || m.anchor.valid || !m.followLive || m.unread != 0 {
		t.Fatal("secondary state leaked across session identity")
	}
}

func TestPatternsPreserveFollowingAndRefreshDoesNotResetBrowsing(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = durableRecords(64)
	m.rebuildJournal()
	for i := 0; i < 80; i++ {
		m.patterns = append(m.patterns, fmt.Sprintf("PATTERN-%02d", i))
	}
	assertPatternFrame := func(first int) {
		t.Helper()
		frame := ansi.Strip(m.View().Content)
		if !strings.Contains(frame, fmt.Sprintf("PATTERN-%02d", first)) || strings.Contains(frame, "PATTERN-79") {
			t.Fatalf("pattern frame ignored reading offset %d: %s", first, frame)
		}
		if !m.followLive {
			t.Fatal("pattern frame changed journal follow policy")
		}
	}
	m.Update(key(tea.KeyTab, 0))
	assertPatternFrame(0)
	m.Update(key(tea.KeyPgDown, 0))
	assertPatternFrame(m.viewport.Height())
	m.Update(key(tea.KeyPgUp, 0))
	assertPatternFrame(0)
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	assertPatternFrame(3)
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	assertPatternFrame(0)
	m.Update(key(tea.KeyPgDown, 0))
	offset := m.viewport.YOffset()
	m.started, m.reconnect = true, nil
	m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(1, "JOURNAL-LIVE-TAIL")}})
	assertPatternFrame(offset)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	assertPatternFrame(offset)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	assertPatternFrame(offset)
	if !m.followLive {
		t.Fatal("scrolling secondary panel changed underlying follow policy")
	}
	m.Update(key(tea.KeyTab, 0))
	if !m.followLive || !m.viewport.AtBottom() || !strings.Contains(ansi.Strip(m.View().Content), "JOURNAL-LIVE-TAIL") || strings.Contains(m.View().Content, "PATTERN-") {
		t.Fatal("return did not render the still-following journal")
	}
	m.browse()
	m.unread = 9
	m.refreshViewport(true)
	if m.followLive || m.unread != 9 || !m.viewport.AtBottom() {
		t.Fatal("render repositioning mutated journal policy")
	}
	m.Update(key(tea.KeyTab, 0))
	m.prompt.SetValue("/latest")
	m.Update(key(tea.KeyEnter, 0))
	if m.showPatterns || !m.followLive || m.unread != 0 {
		t.Fatal("/latest did not reset secondary state")
	}
}

func TestLiveRegionAnchorIsNotAssignedToLastDurableRecord(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = durableRecords(1)
	m.stream = []streamEntry{{text: strings.Repeat("LIVE-ONLY\n", 80)}}
	m.rebuildJournal()
	m.browse()
	m.viewport.SetYOffset(10)
	before := m.viewport.View()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if m.anchor.recordID != "" {
		t.Fatal("live content attributed to preceding durable record")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.viewport.View() != before {
		t.Fatal("live-only frozen content moved")
	}
}

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
	if m.anchor.recordID == "" {
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
	m.Update(key('t', tea.ModCtrl)) // Expand selected work, then select its source.
	m.Update(key(tea.KeyDown, tea.ModCtrl))
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
