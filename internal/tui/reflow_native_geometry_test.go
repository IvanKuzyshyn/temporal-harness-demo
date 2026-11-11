package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestReflowBudgetNarrowNativeGeometry(t *testing.T) {
	for _, width := range []int{0, 1} {
		t.Run(fmt.Sprintf("width%d", width), func(t *testing.T) {
			m := nearBudgetReflowModel(t, 60)
			const needle = "ANCHOR-LIVE-059 界 🙂"
			m.stream[59].text = strings.Replace(m.stream[59].text, "ANCHOR-LIVE-059", needle, 1)
			m.rebuildJournal()
			frozen := strings.Join(m.journalParts, "\n")
			m.prompt.SetValue("NARROW DRAFT")
			m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			if !m.journalCompact {
				t.Fatal("fixture must exercise native budget fallback")
			}
			assertReflowRows(t, m)

			// Locate the actual rendered source independently of partDisplayLines.
			// This includes any spaces the heading renderer adds to separators.
			target, found := 0, false
			for _, line := range strings.Split(ansi.Strip(m.viewport.GetContent()), "\n") {
				if strings.Contains(line, needle) {
					found = true
					break
				}
				target += max(1, (ansi.StringWidth(line)+max(1, width)-1)/max(1, width))
			}
			if !found {
				t.Fatal("actual native rendering lost the source sentinel")
			}
			// A real fresh browse gesture must capture this actual row, not a row
			// inferred from the same map whose geometry is under test.
			m.viewport.SetYOffset(target - m.viewport.Height())
			m.Update(key(tea.KeyPgDown, 0))
			if m.viewport.YOffset() != target {
				t.Fatalf("browse gesture landed at %d, want %d", m.viewport.YOffset(), target)
			}
			m.captureAnchor()
			a := m.anchor
			if !a.valid || !strings.Contains(strings.Split(m.journalParts[a.part], "\n")[a.line], needle) {
				t.Errorf("fresh browse captured wrong actual source: %+v", a)
			}
			for _, restored := range []int{80, 120} {
				m.Update(tea.WindowSizeMsg{Width: restored, Height: 24})
				firstRows := strings.Join(strings.Split(ansi.Strip(m.viewport.View()), "\n")[:2], "\n")
				if !strings.Contains(firstRows, needle) {
					t.Errorf("restore %d lost actual source at top: %q", restored, firstRows)
				}
				if m.followLive || strings.Join(m.journalParts, "\n") != frozen || !strings.Contains(ansi.Strip(m.View().Content), "NARROW DRAFT") {
					t.Errorf("restore %d changed frozen source/draft/browse policy", restored)
				}
				if m.journalCompact != (restored == 80) {
					t.Errorf("restore %d did not cross presentation budget", restored)
				}
				assertReflowRows(t, m)
			}
		})
	}
}
