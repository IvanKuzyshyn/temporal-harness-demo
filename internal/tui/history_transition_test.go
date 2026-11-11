package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
)

func TestOlderImmediatelyRendersJournal(t *testing.T) {
	for _, panel := range []string{"journal", "detail", "log"} {
		for _, patterns := range []bool{false, true} {
			for _, available := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/patterns=%v/reader=%v", panel, patterns, available), func(t *testing.T) {
					m, _ := testModel(t)
					if available {
						m.history.dir = t.TempDir()
					}
					records := durableRecords(2)
					records[0].Message.Content = "SELECTED-RECORD"
					records[1].Message.Content = "RETAINED-JOURNAL-ONLY"
					m.history.page = activities.TranscriptPage{Entries: records, Before: 10, Total: 12, HasMore: true}
					m.history.loaded = true
					m.history.observedTotal = 12
					if panel == "detail" {
						m.history.detailID = records[0].ID
					}
					if panel == "log" {
						m.log = logState{ref: "captured-ref", recordID: records[0].ID}
						m.log.page.Text = "LOG-ONLY"
					}
					m.rebuildJournal()
					m.browse()
					m.unread = 7
					m.prompt.SetValue("unsent draft")
					m.patterns = []string{"PATTERN-ONLY"}
					if patterns {
						m.Update(key(tea.KeyTab, 0))
					}
					if m.prompt.Value() != "unsent draft" {
						t.Fatal("panel visit changed draft")
					}
					m.prompt.SetValue("/older")
					_, cmd := m.Update(key(tea.KeyEnter, 0))
					assertJournal := func(draft string, unread uint64) {
						t.Helper()
						body, frame := m.viewport.View(), m.View().Content
						if !strings.Contains(body, "RETAINED-JOURNAL-ONLY") || strings.Contains(body, "PATTERN-ONLY") || strings.Contains(body, "LOG-ONLY") || strings.Contains(body, "Ctrl+O /journal return") {
							t.Fatalf("not the cached journal body: %s", body)
						}
						if !strings.Contains(frame, "Journal · BROWSING") || m.showPatterns || m.history.detailID != "" || m.log.ref != "" {
							t.Fatal("journal chrome/state disagree")
						}
						if m.followLive || m.unread != unread || m.prompt.Value() != draft {
							t.Fatalf("navigation changed follow/unseen/draft: %v %d %q", m.followLive, m.unread, m.prompt.Value())
						}
					}
					assertJournal("", 7) // Only the submitted command resets the prompt.
					m.prompt.SetValue("next draft")
					if available {
						if cmd == nil || !m.history.pending {
							t.Fatal("reader did not start")
						}
						reply := historyMsg{epoch: m.epoch, generation: m.history.generation, query: m.history.query, before: 10, err: fmt.Errorf("injected read failure")}
						stale := reply
						stale.generation--
						m.Update(stale)
						if !m.history.pending {
							t.Fatal("stale reply consumed pending navigation")
						}
						m.Update(reply)
					} else if m.history.pending || m.history.err == "" {
						t.Fatal("unavailable reader not reported")
					}
					assertJournal("next draft", 7)
					// A later poll observes totals but must not replace the cached body.
					m.history.dir = t.TempDir()
					m.loadHistoryForQuery(0, m.queryID)
					tail := activities.TranscriptPage{Entries: durableRecords(1), Total: 15}
					tail.Entries[0].Message.Content = "NEW-TAIL-ONLY"
					m.Update(historyMsg{epoch: m.epoch, generation: m.history.generation, query: m.history.query, page: tail})
					assertJournal("next draft", 10)
					// A subsequent explicit success still replaces the window normally.
					m.loadHistory(10)
					tail.Before = 5
					m.Update(historyMsg{epoch: m.epoch, generation: m.history.generation, query: m.history.query, before: 10, page: tail})
					if !strings.Contains(m.viewport.View(), "NEW-TAIL-ONLY") || strings.Contains(m.viewport.GetContent(), "RETAINED-JOURNAL-ONLY") || m.followLive || m.unread != 10 || m.prompt.Value() != "next draft" {
						t.Fatal("explicit success did not replace only the journal window")
					}
				})
			}
		}
	}
}
