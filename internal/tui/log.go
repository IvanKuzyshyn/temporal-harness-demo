package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
)

type logState struct {
	ref, recordID, err string
	generation         uint64
	pending            bool
	page               activities.CommandLogPage
}
type logMsg struct {
	epoch, generation uint64
	ref, recordID     string
	page              activities.CommandLogPage
	err               error
}

// Interpret only the canonical parsed representation whose outcome/identity was
// validated. In particular read_file bytes and mixed/aliased schemas are never
// artifact envelopes, and no whole-struct second decode can pick a different key.
func capturedLogRef(name, callID, content string) string {
	if name != "run_command" {
		return ""
	}
	fields, ok := strictObject(content, "toolCallId", "output", "structured", "details", "isError", "interrupted", "exitCode", "executionError", "tail", "logRef", "truncated")
	if !ok || strings.HasPrefix(toolOutcome(name, callID, fields), "outcome unavailable") {
		return ""
	}
	source := fields
	if _, ordinary := fields["toolCallId"]; ordinary {
		source, ok = strictObject(string(fields["details"]), "exitCode", "executionError", "logRef", "truncated")
		if !ok {
			return ""
		}
	}
	var ref string
	if !strictField(source["logRef"], &ref) {
		return ""
	}
	return ref
}
func (m *Model) openSelectedLog() tea.Cmd {
	selected := m.history.detailID
	if selected == "" {
		selected = m.selectedID
	}
	for _, r := range m.history.page.Entries {
		if r.ID != selected {
			continue
		}
		ref := capturedLogRef(r.Message.ToolName, r.Message.ToolCallID, r.Message.Content)
		if ref == "" {
			m.notice = "No validated command log on selected record"
			return nil
		}
		m.browse()
		m.log = logState{ref: ref, recordID: r.ID, generation: m.log.generation + 1}
		return m.loadLog(0)
	}
	m.notice = "Select a command result with Ctrl+Up/Down or /detail N first"
	return nil
}
func (m *Model) pageLog(direction int) tea.Cmd {
	if m.log.ref == "" || m.log.pending {
		return nil
	}
	offset := max(int64(0), m.log.page.Offset-activities.MaxCommandLogPage)
	if direction > 0 {
		if !m.log.page.HasMore {
			m.notice = "End of available log page range"
			return nil
		}
		offset = m.log.page.Next
	}
	return m.loadLog(offset)
}
func (m *Model) loadLog(offset int64) tea.Cmd {
	m.logGeneration++
	m.log.generation = m.logGeneration
	m.log.pending = true
	m.log.err = ""
	if m.logCancel != nil {
		m.logCancel()
	}
	readCtx, stopRead := context.WithCancel(m.ctx)
	m.logCancel = stopRead
	epoch, generation, ref, recordID, dir, id := m.epoch, m.log.generation, m.log.ref, m.log.recordID, m.history.dir, m.opts.SessionID
	m.rebuildJournal()
	return func() tea.Msg {
		defer stopRead()
		ctx, cancel := context.WithTimeout(readCtx, requestTimeout)
		defer cancel()
		page, err := activities.ReadCommandLogPage(ctx, dir, id, ref, offset)
		return logMsg{epoch: epoch, generation: generation, ref: ref, recordID: recordID, page: page, err: err}
	}
}
func (m *Model) acceptLog(msg logMsg) {
	if msg.epoch != m.epoch || msg.generation != m.log.generation || msg.ref != m.log.ref || msg.recordID != m.log.recordID || !m.log.pending {
		return
	}
	m.log.pending = false
	if msg.err != nil {
		m.log.err = bounded(safeText(msg.err.Error()), 1024)
	} else {
		m.log.page = msg.page
	}
	m.rebuildJournal()
	m.viewport.GotoTop()
}
func (m *Model) logView() string {
	p := m.log.page
	header := fmt.Sprintf("Captured command log · bytes %d–%d / %d\nCtrl+Left/Right pages · PgUp/PgDn scroll · Ctrl+O journal · End latest", p.Offset, p.Next, p.Size)
	if p.HasMore {
		header += "\n[more log bytes available; next page is not truncation]"
	}
	if p.Truncated {
		header += "\n[artifact exceeds 64 MiB browsing limit; remainder unavailable here]"
	}
	if m.log.pending {
		header += "\n[loading]"
	}
	if m.log.err != "" {
		header += "\n[log unavailable; cached page if any] " + m.log.err
	}
	return header + "\n" + safeText(p.Text)
}
