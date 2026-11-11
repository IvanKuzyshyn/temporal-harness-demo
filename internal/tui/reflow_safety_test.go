package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Own the potentially stuck event loop in a subprocess, not a goroutine that
// can survive the test timeout and exhaust the parent test process's memory.
func TestReflowNarrowUnicodeCompletes(t *testing.T) {
	if os.Getenv("TUI_REFLOW_CHILD") == "1" {
		for _, frozen := range []bool{false, true} {
			for _, width := range []int{0, 1} {
				for _, body := range []string{"界 tail", "🙂 tail", "prefix 界 tail", "prefix 👩‍💻 tail"} {
					m, _ := testModel(t)
					r := durableRecords(1)
					if frozen {
						r = durableRecords(2)
						r[1].Message.Content = strings.Repeat("later\n", 20)
					}
					r[0].Message.Content = body
					m.history.page.Entries = r
					m.rebuildJournal()
					if frozen {
						m.browse()
						m.viewport.SetYOffset(2) // Actual first body, not the history header.
						m.captureAnchor()
						if m.anchor.recordID != r[0].ID || m.anchor.line != 1 {
							t.Fatal("narrow fixture did not anchor actual source")
						}
					}
					m.prompt.SetValue("DRAFT KEPT")
					m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
					assertReflowRows(t, m)
					for _, restored := range []int{80, 120} {
						m.Update(tea.WindowSizeMsg{Width: restored, Height: 24})
						frame := ansi.Strip(m.View().Content)
						if !strings.Contains(frame, safeText(body)) || !strings.Contains(frame, "DRAFT KEPT") || m.followLive == frozen {
							t.Fatalf("frozen=%v width=%d restore=%d lost actual source/draft/policy: %q", frozen, width, restored, frame)
						}
						assertReflowRows(t, m)
					}
					m.Close()
				}
			}
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReflowNarrowUnicodeCompletes$", "-test.timeout=6s")
	cmd.Env = append(os.Environ(), "TUI_REFLOW_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("narrow reflow did not complete safely: %v (deadline: %v)\n%s", err, ctx.Err(), out)
	}
}

func assertReflowRows(t *testing.T, m *Model) {
	t.Helper()
	rows := 0
	for i := range m.journalParts {
		rows += len(m.partDisplayLines(i))
	}
	if got := m.viewport.TotalLineCount(); got != rows {
		t.Errorf("render/navigation mismatch: viewport=%d mapped=%d", got, rows)
	}
	if size := len(m.viewport.GetContent()); size > maxJournalText {
		t.Errorf("aggregate display exceeds %d bytes: %d", maxJournalText, size)
	}
}

func nearBudgetReflowModel(t *testing.T, liveCount int) *Model {
	t.Helper()
	m, _ := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	r := durableRecords(64)
	for i := range r {
		r[i].Message.Content = strings.Repeat("word ", 800)
	}
	m.history.page.Entries = r
	m.history.loaded = true
	m.expandedWork = map[string]bool{"work:live": true}
	var numbered strings.Builder
	for n := 0; n < 780; n++ {
		fmt.Fprintf(&numbered, "%04d ", n)
	}
	for i := 0; i < liveCount; i++ {
		m.stream = append(m.stream, streamEntry{tool: true, label: fmt.Sprintf("Live observation %d", i), text: numbered.String() + fmt.Sprintf("\nANCHOR-LIVE-%03d\n", i) + strings.Repeat("end\n", 20)})
	}
	m.rebuildJournal()
	return m
}

func TestReflowBudgetPreservesActualFrozenAnchor(t *testing.T) {
	m := nearBudgetReflowModel(t, 60)
	m.browse()
	needle := "ANCHOR-LIVE-059"
	row, target := 0, -1
	for i := range m.journalParts {
		for _, line := range m.partDisplayLines(i) {
			if strings.Contains(line.text, needle) {
				target = row
			}
			row++
		}
	}
	if target < 0 {
		t.Fatal("fixture lost canonical tail before reflow")
	}
	m.viewport.SetYOffset(target)
	m.captureAnchor()
	frozen := strings.Join(m.journalParts, "\n")
	anchor := m.anchor
	if !strings.Contains(strings.Split(m.journalParts[anchor.part], "\n")[anchor.line], needle) {
		t.Fatal("fixture must anchor the sentinel itself, not a bottom-clamped earlier row")
	}
	m.prompt.SetValue("BUDGET DRAFT")
	m.stream[59].text = "INCOMING LIVE REPLACEMENT"
	m.history.page.Entries[0].Message.Content = "INCOMING DURABLE REPLACEMENT"
	for _, width := range []int{120, 80, 120} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		frame := ansi.Strip(m.View().Content)
		if !strings.Contains(frame, needle) || !strings.Contains(frame, "BUDGET DRAFT") {
			t.Errorf("width %d lost actual anchored source/composer: %q", width, frame)
		}
		if m.anchor.part != anchor.part || m.anchor.line != anchor.line || m.anchor.cell != anchor.cell || strings.Join(m.journalParts, "\n") != frozen {
			t.Errorf("width %d changed frozen canonical window/anchor", width)
		}
		assertReflowRows(t, m)
		t.Logf("width=%d compact=%v canonical=%d rendered=%d mapped rows=%d", width, m.journalCompact, len(frozen), len(m.viewport.GetContent()), m.viewport.TotalLineCount())
		if m.journalCompact != (width == 80) {
			t.Fatal("fixture must cross the formatted display budget at 80, not 120")
		}
	}

	// Also anchor within a long source line, where compact native wrapping
	// includes the original gutter but prose wrapping excludes it. Check unique
	// actual source cells, not just a line-start sentinel or stored anchor fields.
	m.anchor = readingAnchor{}
	row = 0
	for i := range m.journalParts {
		for _, line := range m.partDisplayLines(i) {
			if i == anchor.part && line.line == 1 && line.cell > 1800 {
				target = row
				break
			}
			row++
		}
		if i == anchor.part {
			break
		}
	}
	m.viewport.SetYOffset(target)
	m.captureAnchor()
	anchor = m.anchor
	text := strings.TrimPrefix(strings.Split(m.journalParts[anchor.part], "\n")[anchor.line], "    ")
	fragment := strings.Join(strings.Fields(ansi.Cut(text, anchor.cell, anchor.cell+20)), "")
	if len(fragment) < 12 {
		t.Fatal("fixture needs a unique long-line source fragment")
	}
	for _, width := range []int{80, 120, 80, 120} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		firstRows := strings.Split(ansi.Strip(m.viewport.View()), "\n")[:2]
		actual := strings.Join(strings.Fields(strings.Join(firstRows, "")), "")
		if !strings.Contains(actual, fragment) || m.anchor.cell != anchor.cell || m.anchor.line != anchor.line {
			t.Fatalf("width=%d lost actual source cells %q at %+v: %q", width, fragment, anchor, actual)
		}
		assertReflowRows(t, m)
	}
}

func TestReflowBudgetTruncationNoticeAndTargetsAreMapped(t *testing.T) {
	m := nearBudgetReflowModel(t, 90)
	for _, width := range []int{120, 80, 120} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		assertReflowRows(t, m)
		notice := m.journalParts[len(m.journalParts)-1]
		if !strings.Contains(notice, "window display limit reached") || !strings.Contains(notice, "including live observations") || !strings.Contains(notice, "/detail N for durable records") {
			t.Fatal("aggregate omission notice must be honest, canonical and mapped")
		}
		if m.journalBlocks[len(m.journalBlocks)-1].id != "" {
			t.Fatal("omission notice impersonates a source record")
		}
		if width >= 80 {
			m.viewport.GotoBottom()
			if !strings.Contains(ansi.Strip(m.viewport.View()), "window display limit reached") {
				t.Fatal("mapped omission notice missing from actual viewport")
			}
		}
	}
}
