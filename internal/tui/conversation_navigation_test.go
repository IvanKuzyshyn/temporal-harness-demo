package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

// A projection with five sources has only four visible parts. Any old index+1
// mapping anchors the answer to the tool-request source instead of the answer.
func TestConversationSharedMappingAndFrozenReflow(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[4].Message.Content = strings.Repeat("answer-evidence-", 250)
	following := durableRecords(8)
	for i := range following {
		following[i].ID = fmt.Sprintf("%064x", 100+i)
		following[i].Message.Content = strings.Repeat("following\n", 20)
	}
	m.history.page.Entries = append(r, following...)
	m.rebuildJournal()
	m.browse()
	m.viewport.SetYOffset(8)
	m.captureAnchor()
	if m.anchor.recordID != r[4].ID {
		t.Fatalf("answer anchor mapped to %q instead of source %q", m.anchor.recordID, r[4].ID)
	}
	before := m.viewport.View()
	frozen := m.viewport.GetContent()
	m.prompt.SetValue("draft\nkept")
	m.applyEvent(delta(1, "NEW-INCOMING"))
	m.refreshViewport(false)
	for _, w := range []int{120, 80} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
	}
	if m.viewport.View() != before || m.viewport.GetContent() != frozen || m.prompt.Value() != "draft\nkept" || m.unread != 1 {
		t.Fatal("projection anchor/draft/frozen window drifted")
	}
	// Selecting the answer must point to its visible heading, not a hidden source.
	m.selectedID = r[4].ID
	m.selectRecord(0)
	if !strings.Contains(ansi.Strip(m.viewport.View()), "> ✓ Assistant") {
		t.Fatal("selected source has no visible target")
	}
}

func TestConversationExpansionLifetimeBoundAndResets(t *testing.T) {
	m, _ := testModel(t)
	dir := t.TempDir()
	r := conversationFixture()
	saveHistory(t, dir, m.opts.SessionID, r)
	historySnapshot(t, m, dir)
	m.Update(key('t', tea.ModCtrl))
	if len(m.expandedWork) != 1 {
		t.Fatal("work not expanded")
	}
	m.resumeFollowing()
	historySnapshot(t, m, dir)
	if !strings.Contains(ansi.Strip(m.viewport.GetContent()), "▾ Work") {
		t.Fatal("same-page refresh lost expansion identity")
	}
	// Replacing the tail repeatedly must evict identities outside the 64-record page.
	for n := 0; n < 100; n++ {
		r := conversationFixture()
		for i := range r {
			r[i].ID = fmt.Sprintf("%064x", 1000+n*5+i)
		}
		m.history.page.Entries = r
		m.rebuildJournal()
		m.viewport.GotoTop()
		m.selectedID = ""
		m.Update(key('t', tea.ModCtrl))
		if len(m.expandedWork) > 1 {
			t.Fatal("expansion cache retains evicted pages")
		}
	}
	m.jumpLatest()
	if len(m.expandedWork) != 0 {
		t.Fatal("End retained expansion")
	}
	m.history.page.Entries = conversationFixture()
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	m.loadHistory(1)
	if len(m.expandedWork) != 0 {
		t.Fatal("older retained expansion")
	}
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	m.syncHistory(&workflows.SessionInfo{DataDir: t.TempDir()}, m.queryID)
	if len(m.expandedWork) != 0 {
		t.Fatal("source switch retained expansion")
	}
	m.switchSession("other")
	if len(m.expandedWork) != 0 {
		t.Fatal("session switch retained expansion")
	}
}

func TestConversationExpandedCommandLogReachability(t *testing.T) {
	m, _ := testModel(t)
	m.history.dir = t.TempDir()
	r := conversationFixture()
	r[3].Message = llm.Message{Role: "tool", ToolName: "run_command", ToolCallID: "reused", Content: `{"toolCallId":"reused","output":"RAW","isError":false,"details":{"exitCode":"2","logRef":"logs/captured.log"}}`}
	m.history.page.Entries = r
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	m.selectRecord(1)
	m.selectRecord(1)
	m.selectRecord(1)
	if m.selectedID != r[3].ID {
		t.Fatal("expanded tool not reachable through visible selection")
	}
	_, cmd := m.Update(key('l', tea.ModCtrl))
	if cmd == nil || m.log.recordID != r[3].ID || m.log.ref != "logs/captured.log" {
		t.Fatal("projection broke validated log/source binding")
	}
}

func TestConversationRoleColorsDoNotTrustBody(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[3].Message = llm.Message{Role: "tool", ToolName: "run_command", ToolCallID: "reused", Content: `{"toolCallId":"reused","output":"captured","isError":false,"details":{"exitCode":"2"}}`}
	m.history.page.Entries = r
	m.rebuildJournal()
	v := m.viewport.GetContent()
	for _, color := range []string{"38;5;80", "38;5;78", "38;5;203"} {
		if !strings.Contains(v, color) {
			t.Errorf("missing semantic color %s", color)
		}
	}
	r[3].Message.Content = "unknown"
	m.rebuildJournal()
	if !strings.Contains(m.viewport.GetContent(), "38;5;214") {
		t.Fatal("unknown outcome not amber")
	}
	r[3].Message = conversationFixture()[3].Message
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	v = m.viewport.GetContent()
	if !strings.Contains(v, "RAW-FILE [succeeded] [failed] Assistant") || strings.Contains(v, "38;5;203") {
		t.Fatal("raw read text controlled semantic coloring")
	}
}

func TestConversationCollapseSelectedMemberRestoresGroupAnchor(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	m.history.page.Entries = append(r, durableRecords(20)...)
	// Use distinct identities, as the durable reader requires.
	for i := 5; i < len(m.history.page.Entries); i++ {
		m.history.page.Entries[i].ID = fmt.Sprintf("%064x", 100+i)
	}
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	m.selectedID = r[3].ID
	m.revealSelected()
	m.captureAnchor()
	m.Update(key('t', tea.ModCtrl))
	if m.selectedID != "work:"+r[0].ID || !strings.Contains(ansi.Strip(m.viewport.View()), "> ▸ Work") {
		t.Fatal("selected hidden member lost group target")
	}
	// No hidden record can accidentally be returned by visible selection.
	m.selectRecord(1)
	if m.selectedID != r[4].ID {
		t.Fatal("selection used old record indices")
	}
}

func TestConversationExpandedSourceAnchorSurvivesResize(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[1].Message.Content = strings.Repeat("context-evidence-", 220)
	following := durableRecords(10)
	for i := range following {
		following[i].ID = fmt.Sprintf("%064x", 100+i)
		following[i].Message.Content = strings.Repeat("following\n", 20)
	}
	m.history.page.Entries = append(r, following...)
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	// The context is now a bounded preview: anchor within its second body
	// row, rather than the old unbounded context's twelfth viewport row.
	m.viewport.SetYOffset(6)
	m.anchor = readingAnchor{}
	m.captureAnchor()
	if m.anchor.recordID != r[1].ID {
		t.Fatal("fixture did not anchor expanded source")
	}
	before := m.viewport.View()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.viewport.View() != before {
		t.Fatal("expanded source anchor snapped to owning group instead of visible source")
	}
}

func TestConversationPartialPageDoesNotInventCompleteOrigin(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	m.history.page = activities.TranscriptPage{Before: 25, Entries: r[1:], Total: 29}
	m.rebuildJournal()
	if !strings.Contains(m.viewport.GetContent(), "earlier / partial") {
		t.Fatal("partial origin omitted")
	}
	m.viewport.GotoTop()
	m.Update(key('t', tea.ModCtrl))
	m.prompt.SetValue("/detail 28")
	m.submitPrompt(false)
	if m.history.detailID != r[3].ID || !strings.Contains(m.viewport.GetContent(), "RAW-FILE") {
		t.Fatal("global source numbering lost under partial projection")
	}
}
