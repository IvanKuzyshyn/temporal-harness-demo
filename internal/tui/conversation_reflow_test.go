package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestVisualLiteralDetailKeepsNativeCellAnchor(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[1].Message.Content = strings.Repeat(strings.Repeat("x", 79)+"界", 200)
	m.history.page.Entries = r
	m.prompt.SetValue("/detail 2")
	m.submitPrompt(false)
	// Raw details still use the viewport's cell slicing, not word-wrap. At 80
	// columns, each row advances 80 source cells even across wide glyphs.
	m.viewport.SetYOffset(10)
	m.captureAnchor()
	if m.anchor.cell%80 != 0 {
		t.Fatalf("literal detail anchor used grapheme wraps instead of native cell slices: %+v", m.anchor)
	}
	before := m.viewport.View()
	frozen := strings.Join(m.journalParts, "\n")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.viewport.View() != before || strings.Join(m.journalParts, "\n") != frozen {
		t.Fatal("literal detail drifted on round trip")
	}
}

func TestVisualSelectionRevealsHeadingNotOnlyItsSeparator(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[0].Message.Content = strings.Repeat("prompt\n", 9)
	following := durableRecords(10)
	for i := range following {
		following[i].ID = "following-" + following[i].ID
	}
	m.history.page.Entries = append(r, following...)
	m.rebuildJournal()
	m.selectedID = r[4].ID
	m.selectRecord(0)
	if !strings.Contains(ansi.Strip(m.View().Content), "> ✓ Assistant") {
		t.Fatal("selection revealed only the blank separator, not its heading")
	}
}

func TestVisualWordWrappedFrozenSourceAndSelection(t *testing.T) {
	m, _ := testModel(t)
	r := durableRecords(12)
	var body strings.Builder
	for i := 0; i < 100; i++ {
		body.WriteString("short extraordinary medium ")
	}
	r[0].Message.Content = body.String()
	for i := 1; i < len(r); i++ {
		r[i].Message.Content = strings.Repeat("later\n", 20)
	}
	m.history.page.Entries = r
	m.rebuildJournal()
	m.browse()
	m.viewport.SetYOffset(18)
	m.captureAnchor()
	before := m.viewport.View()
	anchor := m.anchor
	frozen := strings.Join(m.journalParts, "\n")
	m.prompt.SetValue("DRAFT\nKEPT")
	m.history.page.Entries[0].Message.Content = "REPLACED"
	for _, width := range []int{120, 81, 119, 80} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		if m.anchor.recordID != r[0].ID || m.anchor.line != anchor.line || m.anchor.cell != anchor.cell || strings.Join(m.journalParts, "\n") != frozen {
			t.Fatal("word wrap changed frozen source/anchor")
		}
		frame := ansi.Strip(m.View().Content)
		if strings.Contains(frame, "REPLACED") || !strings.Contains(frame, "KEPT") {
			t.Fatal("reflow consumed live page or lost composer")
		}
	}
	if m.viewport.View() != before {
		t.Fatal("word-wrapped round trip drifted")
	}
}
