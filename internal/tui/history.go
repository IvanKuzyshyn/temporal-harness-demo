package tui

import (
	"context"
	"fmt"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// A single complete page replaces the previous window. No concatenated gaps or
// unbounded session cache; /older walks backward, /latest returns to the tail.
type historyState struct {
	dir                    string // ORIGINAL metadata, never the sanitized display copy
	cancel                 context.CancelFunc
	generation, query      uint64
	pending, older, loaded bool
	jumpLatest             bool
	explicit               bool
	observedTotal          int
	page                   activities.TranscriptPage
	err                    string
	detailID               string   // selection follows durable identity, not a row offset
	retired                []string // bounded completed MessageRef identities, independent of page
}
type historyMsg struct {
	epoch, generation, query uint64
	before                   int
	page                     activities.TranscriptPage
	err                      error
}

func (m *Model) syncHistory(session *workflows.SessionInfo, query uint64) tea.Cmd {
	dir := ""
	if session != nil {
		dir = session.DataDir
	}
	if dir != m.history.dir {
		previousDir := m.history.dir
		if previousDir == "" {
			previousDir = m.opts.DataDir
		}
		if dir != previousDir {
			m.offset = 0
			if previousDir != "" {
				m.stream = nil
				m.newMessage = true
			}
			m.pending = nil
			m.recent = nil
			if m.reconnect != nil {
				m.reconnect = events.NewReconnect()
			}
		}
		if m.history.cancel != nil {
			m.history.cancel()
		}
		m.history = historyState{dir: dir, generation: m.history.generation + 1}
		m.journalParts = nil
		m.journalBlocks = nil
		m.expandedWork = nil
		m.browseStream = nil
		m.followLive, m.unread = true, 0
		m.selectedID = ""
		m.anchor = readingAnchor{}
		m.showPatterns = false
		m.log = logState{}
	}
	if !filepath.IsAbs(dir) {
		m.history.err = "unavailable: original session DataDir is missing or relative; client directory is not a fallback"
		return nil
	}
	// Do not invalidate a read on every two-second status poll. Its captured
	// query generation remains valid until it completes or metadata/window/session
	// changes. This is distinct from queryID (the newest status request).
	if m.history.pending {
		return nil
	}
	return m.loadHistoryForQuery(0, query)
}
func (m *Model) loadHistory(before int) tea.Cmd {
	// Explicit navigation supersedes an in-flight poll/window. Generation guards
	// discard the old reply; End must not be swallowed by a pending older read.
	if m.history.cancel != nil {
		m.history.cancel()
	}
	m.history.pending = false
	m.history.detailID = ""
	m.selectedID = ""
	m.expandedWork = nil
	m.anchor = readingAnchor{}
	m.showPatterns = false
	m.history.explicit = true
	m.log = logState{}
	if before > 0 {
		m.browse()
	} else {
		m.resumeFollowing()
	}
	m.history.jumpLatest = before == 0
	cmd := m.loadHistoryForQuery(before, m.queryID)
	// Explicit navigation must replace the previous panel immediately, even
	// when the reader cannot start or later fails. Rebuild from the cached page,
	// not the retained parts, which may still contain a detail or captured log.
	if before > 0 {
		m.rebuildJournal()
		m.viewport.GotoTop()
	}
	return cmd
}
func (m *Model) loadHistoryForQuery(before int, query uint64) tea.Cmd {
	if m.history.pending {
		return nil
	}
	if !filepath.IsAbs(m.history.dir) {
		m.history.err = "unavailable: original session DataDir is missing or relative"
		m.history.explicit = false
		m.history.jumpLatest = false
		m.refreshViewport(false)
		return nil
	}
	m.history.generation++
	m.history.query = query
	m.history.pending = true
	m.refreshViewport(false)
	readCtx, stopRead := context.WithCancel(m.ctx)
	m.history.cancel = stopRead
	epoch, generation, dir, id := m.epoch, m.history.generation, m.history.dir, m.opts.SessionID
	return func() tea.Msg {
		defer stopRead()
		ctx, cancel := context.WithTimeout(readCtx, requestTimeout)
		defer cancel()
		page, err := activities.ReadTranscriptPage(ctx, dir, id, before, activities.MaxTranscriptPage)
		return historyMsg{epoch: epoch, generation: generation, query: query, before: before, page: page, err: err}
	}
}
func (m *Model) acceptHistory(msg historyMsg) {
	if msg.epoch != m.epoch || msg.generation != m.history.generation || msg.query != m.history.query || !m.history.pending {
		return
	}
	m.history.pending = false
	if msg.err != nil {
		m.history.err = bounded(safeText(msg.err.Error()), maxText)
	} else {
		if !m.followLive && m.history.loaded && !m.history.explicit {
			if msg.page.Total > m.history.observedTotal {
				m.unread = min(999999, m.unread+uint64(msg.page.Total-m.history.observedTotal))
			}
			m.history.observedTotal = max(m.history.observedTotal, msg.page.Total)
			m.history.err = ""
			return // Keep the exact page/reading anchor, even if the new tail evicts it.
		}
		m.history.observedTotal = msg.page.Total
		changedWindow := m.history.older != (msg.before > 0) || (msg.before > 0 && m.history.page.Before != msg.page.Before)
		m.history.page = msg.page
		m.reconcileStreams()
		if changedWindow {
			m.expandedWork = nil
			m.history.detailID = ""
		}
		m.history.loaded = true
		m.history.older = msg.before > 0
		m.history.err = ""
		m.rebuildJournal()
		m.refreshViewport(false)
		if !m.showPatterns && (changedWindow || m.history.jumpLatest) {
			if m.history.older {
				m.viewport.GotoTop()
			} else {
				m.viewport.GotoBottom()
			}
		}
	}
	m.history.explicit = false
	m.history.jumpLatest = false
	m.refreshViewport(false)
}
func (m *Model) historyHeader() string {
	h := m.history
	text := "History: not loaded · /latest retry"
	if h.loaded {
		text = fmt.Sprintf("History: records %d–%d of %d", h.page.Before+1, h.page.Before+len(h.page.Entries), h.page.Total)
		if len(h.page.Entries) == 0 {
			text = "History: empty"
		}
		if h.older {
			text += " [older window; live updates hidden]"
		}
		if h.page.HasMore {
			text += " · /older"
		}
		text += " · /latest"
	}
	if h.pending {
		text += " [loading]"
	}
	if h.err != "" {
		text += " [unavailable; cached page if any: " + h.err + "]"
	}
	return text
}
