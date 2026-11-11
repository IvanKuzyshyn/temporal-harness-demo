package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Prefer the distinguishing suffix rather than a shared session-name prefix.
func shortSession(id string) string {
	id = strings.TrimPrefix(singleLine(id), "session-")
	if ansi.StringWidth(id) <= 12 {
		return id
	}
	return "…" + ansi.Cut(id, ansi.StringWidth(id)-11, ansi.StringWidth(id))
}

func middleElide(text string, width int) string {
	n := ansi.StringWidth(text)
	if n <= width {
		return text
	}
	left := min(16, max(0, (width-1)/3))
	return ansi.Truncate(text, left, "") + "…" + ansi.Cut(text, n-max(0, width-left-1), n)
}

// Chrome is measured in terminal cells. Full original identities remain in
// /metadata; neither client choices nor elided display values become authority.
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
	model, root := m.opts.Model, m.opts.Input.Workdir
	origin := "selected · config unknown"
	if m.state.Session != nil {
		model, root = m.state.Session.Model, m.state.Session.Workdir
		origin = "original session"
	}
	mode := m.state.Mode
	if mode == "" {
		mode = "unknown"
	}
	if mode == "demo" {
		mode = "demo (enforced)"
	}
	left := "Harness / " + singleLine(mode)
	right := "model:" + ansi.Truncate(singleLine(model), max(8, m.width/4), "…") + " · session:" + shortSession(m.opts.SessionID)
	left = ansi.Truncate(left, max(8, m.width-ansi.StringWidth(right)-2), "…")
	brand := left + strings.Repeat(" ", max(1, m.width-ansi.StringWidth(left+right))) + right
	nav := "Journal · LIVE"
	if !m.followLive {
		nav = fmt.Sprintf("Journal · BROWSING · %d unseen updates · End latest", m.unread)
	}
	if m.log.ref != "" {
		nav = fmt.Sprintf("Captured command log · %d unseen updates · End latest", m.unread)
	} else if m.history.detailID == historyDiagnostic {
		nav = fmt.Sprintf("History diagnostic · %d unseen updates · End latest", m.unread)
	} else if m.history.detailID != "" {
		nav = fmt.Sprintf("Record detail · %d unseen updates · End latest", m.unread)
	}
	if m.showPatterns {
		nav = fmt.Sprintf("Pattern log · Tab return · %d unseen updates", m.unread)
	}
	top = []string{brand}
	if m.height >= 14 {
		// Retain both observed freshness and actual phase, without a second metadata
		// stack. Polling itself never supplies an activity spinner.
		phase := m.state.Phase
		if phase == "" {
			phase = "phase unknown"
		}
		end := m.state.RunFinishedAt
		clock := "run " + elapsed(m.state.RunStartedAt, end)
		pulse := ""
		if status == "working" || status == "blocked" {
			pulse = ansi.Strip(m.spinner.View()) + " "
			if end.IsZero() {
				end = m.now
			}
			clock = "phase " + elapsed(m.state.PhaseStartedAt, end)
		}
		freshness := "heartbeat unknown"
		if !m.heartbeat.IsZero() {
			freshness = "heartbeat " + elapsed(m.heartbeat, m.now) + " ago"
		}
		activity := fmt.Sprintf("%s%s · turn:%d/%d · %s · %s · %s", pulse, status, m.state.Turn, turns, phase, clock, freshness)
		project := "project " + middleElide(singleLine(root), max(1, m.width-ansi.StringWidth("project  · "+origin))) + " · " + origin
		top = append(top, project, activity, nav, strings.Repeat("─", m.width))
	} else {
		top = append(top, nav)
	}
	notice := m.notice
	if notice == "" && m.opts.Demo {
		notice = "Guided demo is opt-in: /demo · use a disposable sample copy"
	}
	if m.history.err != "" {
		notice = "History unavailable · /latest retry · /history-error details"
	}
	if m.height >= 14 {
		if m.configMismatch {
			top = append(top, "Config mismatch: client selection not applied · /metadata")
		}
		if len(m.state.Inbox) > 0 {
			top = append(top, m.queueView())
		}
		if notice != "" {
			top = append(top, notice)
		}
		if m.requestFailure != "" {
			top = append(top, "Request failed · "+m.requestFailure)
		}
	}
	bottom = "Enter send · Alt+Enter steer · Esc stop · Ctrl+C detach · /help"
	for i := range top {
		top[i] = ansi.Truncate(singleLine(top[i]), m.width, "…")
	}
	return top, ansi.Truncate(bottom, m.width, "…")
}
func (m *Model) composerRule() string {
	if m.height < 7 {
		return ""
	}
	return ansi.Truncate("─ Ctrl+T work · Ctrl+↑/↓ select · Ctrl+O detail · PgUp/PgDn · End latest "+strings.Repeat("─", m.width), m.width, "")
}
func (m *Model) journalHeight() int {
	top, bottom := m.chrome()
	reserved := len(top) + lipgloss.Height(m.prompt.View())
	if bottom != "" {
		reserved++
	}
	if m.composerRule() != "" {
		reserved++
	}
	return max(0, m.height-reserved)
}

// Synchronize interaction geometry with the most recently visible frame. A
// queue/notice may change chrome without a terminal resize; controls must not
// select work hidden below that frame or page by an obsolete height.
func (m *Model) syncViewportHeight() {
	h := m.journalHeight()
	if h == m.viewport.Height() {
		return
	}
	m.viewport.SetHeight(h)
	if m.followLive && !m.showPatterns {
		m.viewport.GotoBottom()
	}
}

func (m *Model) resize(w, h int) {
	m.width = max(0, min(w, 1000))
	m.height = max(0, min(h, 500))
	m.prompt.SetWidth(max(4, m.width))
	m.prompt.SetHeight(min(3, max(1, m.height/7)))
	m.viewport.SetWidth(max(1, m.width))
	m.viewport.SetHeight(m.journalHeight())
	m.refreshViewport(false)
	if !m.followLive {
		m.renderJournal() // Reflow the frozen canonical window, not a fresh projection.
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
					rows[i] = semanticHeading(row, "muted")
				} else {
					rows[i] = attentionStyle.Render(row)
				}
			}
			if strings.HasPrefix(row, "History unavailable") {
				rows[i] = semanticHeading(row, "failure")
			}
			if m.requestFailure != "" && row == ansi.Truncate(singleLine("Request failed · "+m.requestFailure), m.width, "…") {
				rows[i] = semanticHeading(row, "failure")
			}
			if strings.HasPrefix(row, "project ") || strings.HasPrefix(row, "─") {
				rows[i] = semanticHeading(row, "muted")
			}
		}
		// Notices/queue can change between resizes. Measure the actual frame, not
		// stale chrome geometry, so exceptional status never pushes drafts offscreen.
		viewport := m.viewport
		viewport.SetHeight(m.journalHeight())
		if m.followLive && !m.showPatterns {
			viewport.GotoBottom()
		}
		if viewport.Height() > 0 {
			rows = append(rows, viewport.View())
		}
		if rule := m.composerRule(); rule != "" {
			rows = append(rows, semanticHeading(rule, "muted"))
		}
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
