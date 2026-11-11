package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Chrome is measured before allocating the journal. Every identity is explicitly
// elided, never wrapped into the composer; /metadata exposes the complete values.
func (m *Model) chrome() (top []string, bottom string) {
	if m.height < 7 {
		return nil, ""
	}
	status := m.state.Status
	if status == "" {
		status = "new"
	}
	turns := m.state.MaxTurns
	if turns == 0 {
		turns = m.opts.Input.MaxTurns
	}
	identity := fmt.Sprintf("Harness · session:%s turn:%d/%d %s", singleLine(m.opts.SessionID), m.state.Turn, turns, status)
	config := strings.Split(m.sessionConfiguration(), "\n")
	nav := "Journal · LIVE"
	if !m.followLive {
		nav = fmt.Sprintf("Journal · BROWSING · %d unseen updates · End latest", m.unread)
	}
	if m.log.ref != "" {
		nav = fmt.Sprintf("Captured command log · %d unseen updates · End latest", m.unread)
	} else if m.history.detailID != "" {
		nav = fmt.Sprintf("Record detail · %d unseen updates · End latest", m.unread)
	}
	if m.showPatterns {
		nav = fmt.Sprintf("Pattern log · Tab return · %d unseen updates", m.unread)
	}
	top = []string{identity}
	if m.height >= 14 {
		top = append(top, config...)
		top = append(top, strings.Split(m.progressView(), "\n")...)
		top = append(top, nav)
	} else {
		top = append(top, nav)
	}
	notice := m.notice
	if notice == "" && m.opts.Demo {
		notice = "Guided demo is opt-in: /demo · use a disposable sample copy"
	}
	if m.history.err != "" {
		notice = "History unavailable: " + m.history.err
	}
	if m.height >= 14 {
		top = append(top, m.queueView(), notice)
	}
	bottom = "Enter send · Alt+Enter steer · Esc stop · Ctrl+C detach · /help"
	if m.height >= 18 {
		top = append(top, "PgUp/PgDn scroll · End live · Ctrl+↑/↓ select · Ctrl+O detail · /older")
	}
	for i := range top {
		top[i] = ansi.Truncate(singleLine(top[i]), m.width, "…")
	}
	return top, ansi.Truncate(bottom, m.width, "…")
}
func (m *Model) resize(w, h int) {
	m.width = max(0, min(w, 1000))
	m.height = max(0, min(h, 500))
	m.prompt.SetWidth(max(4, m.width))
	m.prompt.SetHeight(min(3, max(1, m.height/7)))
	top, bottom := m.chrome()
	reserved := len(top) + lipgloss.Height(m.prompt.View())
	if bottom != "" {
		reserved++
	}
	m.viewport.SetWidth(max(1, m.width))
	m.viewport.SetHeight(max(0, m.height-reserved))
	m.refreshViewport(false)
	if !m.followLive {
		m.restoreAnchor()
	}
}
func (m *Model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return tea.NewView("")
	}
	var content string
	if m.state.PendingApproval != nil {
		content = m.approvalScreen()
	} else {
		top, bottom := m.chrome()
		rows := append([]string(nil), top...)
		for i, row := range rows {
			if i == 0 {
				rows[i] = identityStyle.Render(row)
			}
			if strings.HasPrefix(row, "Journal ·") {
				if m.followLive {
					rows[i] = identityStyle.Render(row)
				} else {
					rows[i] = attentionStyle.Render(row)
				}
			}
		}
		if m.viewport.Height() > 0 {
			rows = append(rows, m.viewport.View())
		}
		// In tiny terminals the composer has priority over all chrome.
		rows = append(rows, m.prompt.View())
		if bottom != "" {
			rows = append(rows, bottom)
		}
		content = strings.Join(rows, "\n")
	}
	view := tea.NewView(lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(content))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}
