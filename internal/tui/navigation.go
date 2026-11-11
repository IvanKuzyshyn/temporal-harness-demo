package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Browsing freezes one bounded rendered window as well as its durable page.
// This preserves live drafts too when a snapshot reconstructs or retires them.
// anchorID + anchorRow describe the top durable record across width changes.
func (m *Model) browse() { m.followLive = false }
func (m *Model) jumpLatest() tea.Cmd {
	m.showPatterns = false
	m.followLive = true
	m.unread = 0
	m.history.detailID = ""
	m.selectedID = ""
	m.log = logState{}
	m.refreshViewport(true)
	return m.loadHistory(0)
}
func (m *Model) captureAnchor() {
	m.anchorID = ""
	m.anchorRow = 0
	row := 0
	for i, part := range m.journalParts {
		n := len(strings.Split(ansi.Hardwrap(part, max(1, m.width), true), "\n"))
		if row <= m.viewport.YOffset() && i > 0 && i <= len(m.history.page.Entries) {
			m.anchorID = m.history.page.Entries[i-1].ID
			m.anchorRow = m.viewport.YOffset() - row
		}
		row += n
	}
}
func (m *Model) selectRecord(direction int) {
	records := m.history.page.Entries
	if len(records) == 0 {
		m.notice = "No loaded records; /latest retries history"
		return
	}
	m.browse()
	m.showPatterns = false
	m.log = logState{}
	index := -1
	for i, r := range records {
		if r.ID == m.selectedID {
			index = i
			break
		}
	}
	if index < 0 {
		index = len(records) - 1
	} else {
		index = max(0, min(len(records)-1, index+direction))
	}
	m.selectedID = records[index].ID
	m.notice = "Selected record · Ctrl+O expand/collapse · Ctrl+L captured log"
	if m.history.detailID != "" {
		m.history.detailID = m.selectedID
	}
	m.rebuildJournal()
	m.viewport.GotoTop()
	if m.history.detailID == "" {
		row := 0
		for i := 0; i <= index && i < len(m.journalParts); i++ {
			row += len(strings.Split(ansi.Hardwrap(m.journalParts[i], max(1, m.width), true), "\n"))
		}
		m.viewport.SetYOffset(row)
	}
}
func (m *Model) toggleDetail() {
	m.showPatterns = false
	if m.log.ref != "" {
		m.log = logState{}
		m.history.detailID = ""
		m.rebuildJournal()
		m.viewport.GotoTop()
		return
	}
	if m.selectedID == "" {
		m.selectRecord(0)
	}
	m.browse()
	m.log = logState{}
	if m.history.detailID != "" {
		m.history.detailID = ""
	} else {
		m.history.detailID = m.selectedID
	}
	m.rebuildJournal()
	m.viewport.GotoTop()
}
func (m *Model) rebuildJournal() {
	if m.log.ref != "" {
		m.journalParts = []string{m.logView()}
	} else if m.showPatterns {
		m.journalParts = []string{strings.Join(m.patterns, "\n")}
	} else {
		m.journalParts = m.journalLines()
	}
	// Retain no more than the bounded display window while browsing. The source
	// live/page caches have their own independent identity and size limits.
	budget := maxJournalText - 256
	for i, part := range m.journalParts {
		if len(part)+1 > budget {
			m.journalParts[i] = journalText(part, max(64, budget)) + "\n[window display limit reached; /detail, /older or End]"
			clear(m.journalParts[i+1:])
			m.journalParts = m.journalParts[:i+1]
			break
		}
		budget -= len(part) + 1
	}
	content := boundedJournal(m.journalParts)
	// Style only validated durable outcome markers, never words in assistant or
	// repository text. The canonical journal/parser remains plain text.
	if m.log.ref == "" && !m.showPatterns {
		styled := append([]string(nil), m.journalParts...)
		for i, r := range m.history.page.Entries {
			index := i + 1
			if m.history.detailID != "" {
				if r.ID != m.history.detailID {
					continue
				}
				index = 2
			}
			if index >= len(styled) || r.Message.Role != "tool" || r.Message.ToolName == "read_file" {
				continue
			}
			text := durableToolText(r.Message.ToolName, r.Message.ToolCallID, r.Message.Content)
			line, _, _ := strings.Cut(text, "\n")
			status := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if status == "succeeded" || status == "failed" || status == "interrupted" || status == "failed/denied (see captured result)" {
				styled[index] = strings.Replace(styled[index], line, outcomeLabel(status), 1)
			}
		}
		content = boundedJournal(styled)
	}
	m.viewport.SetContent(content)
}
