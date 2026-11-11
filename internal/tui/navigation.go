package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Browsing freezes one bounded rendered window as well as its durable page.
// This preserves live drafts too when a snapshot reconstructs or retires them.
// Positions use logical lines and display cells, matching the pinned viewport's
// cell-sliced SoftWrap (not ansi.Hardwrap's grapheme-aware line breaking).
// Keep the original cell through successive resizes: re-capturing a projected
// row at each width would round it down repeatedly and cause reading drift.
type readingAnchor struct {
	recordID                 string
	part, line, cell, offset int
	valid                    bool
}

func wrappedRows(line string, width int) int {
	return max(1, (ansi.StringWidth(line)+max(1, width)-1)/max(1, width))
}
func partRows(part string, width int) int {
	rows := 0
	for _, line := range strings.Split(part, "\n") {
		rows += wrappedRows(line, width)
	}
	return rows
}
func (m *Model) browse() {
	if !m.showPatterns {
		m.followLive = false
		m.anchor = readingAnchor{} // A navigation gesture chooses a new visible top.
	}
}

// Explicit user actions own policy. Rendering and secondary-panel switches do
// not call this; submission/info commands retain their original follow behavior.
func (m *Model) resumeFollowing() {
	m.followLive = true
	m.unread = 0
}
func (m *Model) jumpLatest() tea.Cmd {
	m.showPatterns = false
	m.resumeFollowing()
	m.history.detailID = ""
	m.selectedID = ""
	m.log = logState{}
	m.refreshViewport(true)
	return m.loadHistory(0)
}
func (m *Model) recordForPart(part int) string {
	if m.log.ref != "" {
		return m.log.recordID
	}
	if m.history.detailID != "" {
		return m.history.detailID
	}
	if part > 0 && part <= len(m.history.page.Entries) {
		return m.history.page.Entries[part-1].ID
	}
	return ""
}
func (m *Model) captureAnchor() {
	if m.showPatterns || m.followLive || (m.anchor.valid && m.anchor.offset == m.viewport.YOffset()) {
		return
	}
	m.anchor = readingAnchor{}
	row := 0
	for i, part := range m.journalParts {
		for j, line := range strings.Split(part, "\n") {
			n := wrappedRows(line, m.width)
			if y := m.viewport.YOffset(); y >= row && y < row+n {
				m.anchor = readingAnchor{recordID: m.recordForPart(i), part: i, line: j, cell: (y - row) * max(1, m.width), offset: y, valid: true}
				return
			}
			row += n
		}
	}
}
func (m *Model) restoreAnchor() {
	a := &m.anchor
	if m.showPatterns || !a.valid || a.part >= len(m.journalParts) || m.recordForPart(a.part) != a.recordID {
		return
	}
	row := 0
	for _, part := range m.journalParts[:a.part] {
		row += partRows(part, m.width)
	}
	lines := strings.Split(m.journalParts[a.part], "\n")
	line := min(a.line, len(lines)-1)
	for _, text := range lines[:line] {
		row += wrappedRows(text, m.width)
	}
	// Clamp inside the anchored part/line, not just the whole viewport.
	row += min(a.cell/max(1, m.width), wrappedRows(lines[line], m.width)-1)
	m.viewport.SetYOffset(row)
	a.offset = m.viewport.YOffset()
}

// Patterns are a secondary view over the one retained journal/detail/log
// window, not another history cache or a transition into follow-latest.
func (m *Model) togglePatterns() {
	if !m.showPatterns {
		m.captureAnchor()
		m.showPatterns = true
		m.renderPatterns()
		m.viewport.GotoTop()
		return
	}
	m.showPatterns = false
	m.renderJournal()
	if m.followLive {
		m.viewport.GotoBottom()
	} else {
		m.viewport.GotoTop()
		m.restoreAnchor()
	}
}
func (m *Model) renderPatterns() {
	m.viewport.SetContent(boundedJournal(m.patterns))
}
func (m *Model) selectRecord(direction int) {
	records := m.history.page.Entries
	if len(records) == 0 {
		m.notice = "No loaded records; /latest retries history"
		return
	}
	m.showPatterns = false
	m.browse()
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
			row += partRows(m.journalParts[i], m.width)
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
	m.anchor = readingAnchor{}
	if m.log.ref != "" {
		m.journalParts = []string{m.logView()}
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
	m.renderJournal()
}
func (m *Model) renderJournal() {
	if m.showPatterns {
		return
	}
	content := boundedJournal(m.journalParts)
	// Style only validated durable outcome markers, never words in assistant or
	// repository text. The canonical journal/parser remains plain text.
	if m.log.ref == "" {
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
