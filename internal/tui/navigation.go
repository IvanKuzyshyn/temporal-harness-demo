package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Browsing freezes one bounded rendered window as well as its durable page.
// This preserves live drafts too when a snapshot reconstructs or retires them.
// Positions use canonical logical lines and source cells. The same transient
// row projection drives both native rendering and anchor/selection geometry.
// Keep the original cell through successive resizes: re-capturing a projected
// row at each width would round it down repeatedly and cause reading drift.
type readingAnchor struct {
	recordID                 string
	part, line, cell, offset int
	valid                    bool
}

func (m *Model) browse() {
	if !m.showPatterns {
		if m.followLive {
			m.browseStream = append([]streamEntry(nil), m.stream...)
		}
		m.followLive = false
		m.anchor = readingAnchor{} // A navigation gesture chooses a new visible top.
	}
}

// Explicit user actions own policy. Rendering and secondary-panel switches do
// not call this; submission/info commands retain their original follow behavior.
func (m *Model) resumeFollowing() {
	m.followLive = true
	m.browseStream = nil
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
	if part >= 0 && part < len(m.journalBlocks) {
		return m.journalBlocks[part].id
	}
	return ""
}
func (m *Model) captureAnchor() {
	if m.showPatterns || m.followLive || (m.anchor.valid && m.anchor.offset == m.viewport.YOffset()) {
		return
	}
	m.anchor = readingAnchor{}
	row := 0
	for i := range m.journalParts {
		for _, line := range m.partDisplayLines(i) {
			if y := m.viewport.YOffset(); y == row {
				m.anchor = readingAnchor{recordID: m.recordForPart(i), part: i, line: line.line, cell: line.cell, offset: y, valid: true}
				return
			}
			row++
		}
	}
}
func (m *Model) restoreAnchor() {
	a := &m.anchor
	if m.showPatterns || !a.valid {
		return
	}
	if a.recordID != "" {
		found := false
		for i, b := range m.journalBlocks {
			if b.id == a.recordID {
				a.part = i
				found = true
				break
			}
		}
		// An expanded source has its own exact identity. Membership is only
		// a fallback when collapse has removed that source from visible parts.
		if !found {
			for i, b := range m.journalBlocks {
				for _, id := range b.members {
					if id == a.recordID {
						a.part = i
						a.line, a.cell = 0, 0
						found = true
					}
				}
				if found {
					break
				}
			}
		}
		if !found {
			return
		}
	}
	if a.part >= len(m.journalParts) {
		return
	}
	row := 0
	for i := range m.journalParts[:a.part] {
		row += len(m.partDisplayLines(i))
	}
	// Find the row containing the ORIGINAL cell, without rounding the anchor
	// itself down to the new width's word boundary on each resize.
	lines := m.partDisplayLines(a.part)
	position := 0
	for i, line := range lines {
		if line.line > a.line || (line.line == a.line && line.cell > a.cell) {
			break
		}
		position = i
	}
	row += position
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
	// Detail navigation may visit every durable source; normal navigation visits
	// only visible projection targets, including collapsed work summaries.
	var ids []string
	if m.history.detailID != "" {
		for _, r := range m.history.page.Entries {
			ids = append(ids, r.ID)
		}
	} else {
		for _, b := range m.journalBlocks {
			if b.id != "" {
				ids = append(ids, b.id)
			}
		}
	}
	if len(ids) == 0 {
		m.notice = "No loaded records; /latest retries history"
		return
	}
	m.showPatterns = false
	m.browse()
	m.log = logState{}
	index := -1
	for i, id := range ids {
		if id == m.selectedID {
			index = i
			break
		}
	}
	if index < 0 {
		index = len(ids) - 1
	} else {
		index = max(0, min(len(ids)-1, index+direction))
	}
	m.selectedID = ids[index]
	m.notice = ""
	if m.history.detailID != "" {
		m.history.detailID = m.selectedID
	}
	m.rebuildJournal()
	m.viewport.GotoTop()
	if m.history.detailID == "" {
		m.revealSelected()
	}
}
func (m *Model) toggleDetail() {
	m.showPatterns = false
	if m.history.detailID == historyDiagnostic {
		m.history.detailID = ""
		m.rebuildJournal()
		return
	}
	if m.log.ref != "" {
		m.selectedID = m.log.recordID
		m.log = logState{}
		m.history.detailID = ""
		// Returning from a log exposes its source receipt, preserving Ctrl+L
		// reopen and a visible selection without weakening log/source guards.
		for _, b := range m.conversation() {
			for _, id := range b.members {
				if id == m.selectedID {
					if m.expandedWork == nil {
						m.expandedWork = map[string]bool{}
					}
					m.expandedWork[b.id] = true
				}
			}
		}
		m.rebuildJournal()
		m.revealSelected()
		return
	}
	if m.selectedID == "" {
		m.selectRecord(0)
	}
	if strings.HasPrefix(m.selectedID, "work:") && m.history.detailID == "" {
		m.toggleWork()
		return
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
		m.journalBlocks = []conversationBlock{{id: m.log.recordID, sourceID: m.log.recordID, body: m.logView()}}
	} else {
		m.journalBlocks = m.conversation()
	}
	// Leaving a raw detail for collapsed conversation must retain a visible
	// target. Resolve source membership only after checking all visible IDs.
	visible, owner := false, ""
	for _, b := range m.journalBlocks {
		if b.id == m.selectedID {
			visible = true
		}
		for _, id := range b.members {
			if id == m.selectedID {
				owner = b.id
			}
		}
	}
	if !visible {
		m.selectedID = owner
	}
	m.journalParts = make([]string, len(m.journalBlocks))
	for i, b := range m.journalBlocks {
		m.journalParts[i] = b.text(m.selectedID)
		m.journalBlocks[i].headingBytes = len(safeText(b.heading))
		if b.separate {
			m.journalBlocks[i].headingBytes++
		}
		if b.id != "" && b.id == m.selectedID {
			m.journalBlocks[i].headingBytes += 2
		}
		// Retain one canonical text allocation, not another large detail body.
		m.journalBlocks[i].heading, m.journalBlocks[i].body = "", ""
	}
	// Retain no more than the bounded display window while browsing. The source
	// live/page caches have their own independent identity and size limits.
	budget := maxJournalText - 256
	for i, part := range m.journalParts {
		if len(part)+1 > budget {
			m.journalParts[i] = journalText(part, max(64, budget)) + "\n[window display limit reached; /detail, /older or End]"
			clear(m.journalParts[i+1:])
			m.journalParts = m.journalParts[:i+1]
			clear(m.journalBlocks[i+1:])
			m.journalBlocks = m.journalBlocks[:i+1]
			m.journalBlocks[i].headingBytes = min(m.journalBlocks[i].headingBytes, len(m.journalParts[i]))
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
	styled := make([]string, len(m.journalBlocks))
	for i, b := range m.journalBlocks {
		if b.inset == 0 {
			part := m.journalParts[i]
			styled[i] = semanticHeading(part[:b.headingBytes], b.tone) + part[b.headingBytes:]
			continue // Literal detail/log text retains the viewport's native soft wrap.
		}
		lines := m.partDisplayLines(i)
		text := make([]string, len(lines))
		for j, line := range lines {
			text[j] = line.text
			if line.heading {
				text[j] = semanticHeading(line.text, b.tone)
			}
		}
		styled[i] = strings.Join(text, "\n")
	}
	m.viewport.SetContent(boundedJournal(styled))
}
