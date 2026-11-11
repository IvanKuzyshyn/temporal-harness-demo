package tui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

// safeText never lets untrusted terminal controls reach a renderer. Escape
// introducers, C0/C1 controls, and invisible direction/format controls are
// removed, rather than interpreted as styling. Newlines remain plain text.
func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func singleLine(s string) string {
	return strings.ReplaceAll(safeText(s), "\n", " ")
}

func bounded(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n - len("…")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
func appendBounded(lines []string, s string) []string {
	s = bounded(safeText(s), maxText)
	if len(lines) >= maxEntries {
		copy(lines, lines[1:])
		lines = lines[:maxEntries-1]
	}
	return append(lines, s)
}

// Retain only bounded display data, not potentially large workflow inboxes,
// tool arguments, or child collections returned by the backend.
func displayState(s workflows.State) workflows.State {
	out := workflows.State{Status: bounded(singleLine(s.Status), 128), Turn: s.Turn, MaxTurns: s.MaxTurns, LastSeq: s.LastSeq, Answer: bounded(safeText(s.Answer), maxAnswer)}
	if s.Session != nil {
		out.Session = &workflows.SessionInfo{
			Model:    bounded(singleLine(s.Session.Model), maxText),
			Workdir:  bounded(singleLine(s.Session.Workdir), maxText),
			DataDir:  bounded(singleLine(s.Session.DataDir), maxText),
			Scenario: bounded(singleLine(s.Session.Scenario), maxText),
		}
	}
	if len(s.Inbox) > 0 {
		n := min(len(s.Inbox), 64)
		capacity := n
		// One spare slot records overflow without retaining another entry or
		// repurposing workflow metadata. Only this display projection uses cap
		// as a flag; it is never appended to or sent back to the backend.
		if len(s.Inbox) > n {
			capacity++
		}
		out.Inbox = make([]workflows.MessageInput, n, capacity)
		for i := range out.Inbox {
			out.Inbox[i] = workflows.MessageInput{
				Text:     bounded(singleLine(s.Inbox[i].Text), 256),
				WhenBusy: bounded(singleLine(s.Inbox[i].WhenBusy), 32),
			}
		}
	}
	if s.PendingApproval != nil {
		p := *s.PendingApproval
		p.RequestID = bounded(safeText(p.RequestID), 256)
		p.ActionHash = bounded(safeText(p.ActionHash), 256)
		p.Preview = bounded(safeText(p.Preview), maxText)
		p.Call.ID = bounded(safeText(p.Call.ID), 256)
		p.Call.Name = bounded(safeText(p.Call.Name), 256)
		p.Call.Args = []byte(bounded(safeText(string(p.Call.Args)), maxText))
		p.Decision = nil
		out.PendingApproval = &p
	}
	if s.LastRunFailure != nil {
		out.LastRunFailure = &workflows.Failure{Reason: bounded(safeText(s.LastRunFailure.Reason), maxText), Detail: bounded(safeText(s.LastRunFailure.Detail), maxText)}
	}
	return out
}

func (m *Model) applyEvent(e events.Event) {
	m.lastSeq = e.Seq
	if len(m.recent) >= maxReplayEvents {
		copy(m.recent, m.recent[1:])
		m.recent = m.recent[:maxReplayEvents-1]
	}
	m.recent = append(m.recent, e)
	// Snapshot replay reconstructs assistant deltas, but must not duplicate
	// command output, child announcements, or pattern log entries.
	newLog := e.Seq > m.displaySeq
	m.displaySeq = max(m.displaySeq, e.Seq)
	prefix := ""
	if e.ParentWorkflowID != "" || (e.WorkflowID != "" && e.WorkflowID != m.opts.SessionID) {
		prefix = "  child " + bounded(safeText(e.WorkflowID), 128) + ": "
	}
	if newLog && (e.Pattern != "" && e.Pattern != events.None || e.Type == events.ChildStarted || e.Type == events.ChildCompleted) {
		m.patterns = appendBounded(m.patterns, fmt.Sprintf("%s#%d %s %s", prefix, e.Seq, e.Pattern, e.Type))
	}
	switch e.Type {
	case events.MessageStart, events.MessageUpdate:
		var d events.MessageData
		if json.Unmarshal(e.Data, &d) != nil {
			if e.Type == events.MessageStart {
				m.newMessage = true
			}
			return
		}
		label := prefix + "Assistant: "
		if d.MessageRef != "" && (d.Role == "assistant" || d.Role == "") {
			// Identity lives on bounded entries, so snapshot reconstruction and
			// session switches also reset all attempt-tracking state.
			index := -1
			for i := range m.stream {
				entry := &m.stream[i]
				if entry.messageRef == d.MessageRef && entry.workflowID == e.WorkflowID && entry.parentWorkflowID == e.ParentWorkflowID {
					index = i
					break
				}
			}
			if index < 0 {
				if len(m.stream) >= maxEntries {
					copy(m.stream, m.stream[1:])
					m.stream = m.stream[:maxEntries-1]
				}
				index = len(m.stream)
				m.stream = append(m.stream, streamEntry{label: label, messageRef: d.MessageRef, workflowID: e.WorkflowID, parentWorkflowID: e.ParentWorkflowID})
			}
			entry := &m.stream[index]
			if e.Type == events.MessageStart {
				entry.text = ""
			} else {
				entry.text = bounded(entry.text+safeText(d.Delta), maxAnswer)
			}
			// A subsequent legacy delta must not join this referenced message.
			m.newMessage = true
			return
		}
		if e.Type == events.MessageStart {
			m.newMessage = true
			return
		}
		if d.Delta == "" {
			return
		}
		if m.newMessage || len(m.stream) == 0 || m.stream[len(m.stream)-1].label != label {
			if len(m.stream) >= maxEntries {
				copy(m.stream, m.stream[1:])
				m.stream = m.stream[:maxEntries-1]
			}
			m.stream = append(m.stream, streamEntry{label: label})
		}
		m.newMessage = false
		last := &m.stream[len(m.stream)-1]
		last.text = bounded(last.text+safeText(d.Delta), maxAnswer)
	case events.ToolStart, events.ToolUpdate, events.ToolEnd:
		if !newLog {
			return
		}
		var d events.ToolData
		if json.Unmarshal(e.Data, &d) != nil {
			return
		}
		line := prefix + string(e.Type) + " " + d.Name + " " + d.ToolCallID
		if d.Chunk != "" {
			line += "\n" + d.Chunk
		}
		if d.Summary != "" {
			line += "\n" + d.Summary
		}
		if d.IsError {
			line += " [error]"
		}
		if d.Interrupted {
			line += " [interrupted]"
		}
		m.commands = appendBounded(m.commands, line)
	case events.ChildStarted, events.ChildCompleted:
		if !newLog {
			return
		}
		var d events.ChildData
		if json.Unmarshal(e.Data, &d) == nil {
			m.transcript = appendBounded(m.transcript, "  Child "+d.ChildWorkflowID+": "+string(e.Type)+" "+d.Task)
		}
	}
}

func (m *Model) refreshViewport(forceBottom bool) {
	bottom := forceBottom || m.viewport.AtBottom()
	var content string
	if m.showPatterns {
		content = strings.Join(m.patterns, "\n")
	} else {
		lines := append([]string(nil), m.transcript...)
		if m.state.Answer != "" {
			lines = append(lines, "Assistant (snapshot): "+m.state.Answer)
		}
		for _, entry := range m.stream {
			lines = append(lines, entry.label+entry.text)
		}
		if m.state.LastRunFailure != nil {
			lines = append(lines, "Failure: "+m.state.LastRunFailure.Reason+" "+m.state.LastRunFailure.Detail)
		}
		content = strings.Join(lines, "\n")
	}
	m.viewport.SetContent(content)
	if bottom {
		m.viewport.GotoBottom()
	}
}

// queueView is a single fixed chrome row, independent of transcript scrolling.
// Show the FIFO head, with a second preview when space permits. The count is
// authoritative only up to the projection limit; '+' explicitly marks overflow.
func (m *Model) queueView() string {
	queue := m.state.Inbox
	if len(queue) == 0 {
		return "Queued: 0"
	}
	count := fmt.Sprint(len(queue))
	if cap(queue) > len(queue) {
		count += "+"
	}
	prefix := "Queued: " + count + " "
	n := 1
	if m.width >= 60 && len(queue) > 1 {
		n = 2
	}
	suffix := ""
	if len(queue) > n {
		suffix = fmt.Sprintf(" · +%d more", len(queue)-n)
	}
	budget := max(1, (m.width-ansi.StringWidth(prefix+suffix)-3*(n-1))/n)
	previews := make([]string, n)
	for i := range previews {
		mode := queue[i].WhenBusy
		if mode == "" {
			mode = "followUp"
		}
		mode = ansi.Truncate(mode, 10, "…")
		previews[i] = ansi.Truncate(mode+": "+queue[i].Text, budget, "…")
	}
	return ansi.Truncate(prefix+strings.Join(previews, " · ")+suffix, m.width, "…")
}

func (m *Model) resize(w, h int) {
	// Clamp absurd dimensions before handing them to components that allocate
	// buffers. The outer render still never exceeds the actual terminal size.
	m.width = max(0, min(w, 1000))
	m.height = max(0, min(h, 500))
	m.prompt.SetWidth(max(4, m.width))
	m.prompt.SetHeight(3)
	m.viewport.SetWidth(max(1, m.width))
	m.viewport.SetHeight(max(1, m.height-15))
	m.refreshViewport(false)
}

func (m *Model) sessionMismatch(s *workflows.SessionInfo) bool {
	// An unspecified scenario is not a selection against which to warn. /demo
	// opts in per run and may initialize a fresh session with Scenario=demo.
	return s != nil && (s.Model != m.opts.Model || s.Workdir != m.opts.Input.Workdir || s.DataDir != m.opts.Input.DataDir || (m.opts.Input.Scenario != "" && s.Scenario != m.opts.Input.Scenario))
}

func (m *Model) sessionConfiguration() string {
	s := m.state.Session
	if s == nil {
		return "config:unknown (legacy or not yet queried)\nselected model:" + singleLine(m.opts.Model) + "\nselected root:" + singleLine(m.opts.Input.Workdir)
	}
	text := "model:" + singleLine(s.Model) + "\nroot:" + singleLine(s.Workdir) + "\nconfig:original session"
	if m.configMismatch || m.sessionMismatch(s) {
		text += " [mismatch: client selection not applied]"
	}
	return text
}

func (m *Model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return tea.NewView("")
	}
	if m.state.PendingApproval != nil {
		view := tea.NewView(m.approvalScreen())
		view.AltScreen = true
		return view
	}
	title := lipgloss.NewStyle().Bold(true)
	panel := "Transcript (Tab: patterns)"
	if m.showPatterns {
		panel = "Pattern log (Tab: transcript)"
	}
	commands := "(no commands yet)"
	if m.collapseTools {
		commands = "(collapsed; Ctrl+T to expand)"
	} else if len(m.commands) > 0 {
		// Show the bounded tail; /help and the transcript remain independently scrollable.
		lines := strings.Split(strings.Join(m.commands, "\n"), "\n")
		commands = strings.Join(lines[max(0, len(lines)-2):], "\n")
	}
	commandPane := lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(2).Render(commands)
	if lipgloss.Height(commandPane) < 2 {
		commandPane += "\n"
	}
	status := m.state.Status
	if status == "" {
		status = "new"
	}
	if m.queryPending {
		status += " " + m.spinner.View()
	}
	maxTurns := m.state.MaxTurns
	if maxTurns == 0 {
		maxTurns = m.opts.Input.MaxTurns
	}
	footer := fmt.Sprintf("session:%s turn:%d/%d %s", singleLine(m.opts.SessionID), m.state.Turn, maxTurns, status)
	link := "UI: http://localhost:8233/namespaces/default/workflows/" + url.PathEscape(m.opts.SessionID)
	if lipgloss.Width(link) > m.width {
		link = "UI: http://localhost:8233 (default namespace)"
	}
	banner := "Harness · /help · Esc stop · Ctrl+C quit"
	if m.opts.Demo {
		banner = "Harness · /demo: guided demo · /help · Esc stop · Ctrl+C quit"
	}
	transcriptPane := m.viewport.View()
	content := strings.Join([]string{
		title.Render(banner),
		title.Render(panel), transcriptPane,
		title.Render("Commands (Ctrl+T collapse)"), commandPane,
		m.approvalView(), m.queueView(), m.prompt.View(), footer, link, m.sessionConfiguration(),
	}, "\n")
	// Status and notices are plain text, never terminal instructions.
	if m.notice != "" {
		content = strings.Replace(content, title.Render("Commands (Ctrl+T collapse)"), title.Render("Commands")+" · "+bounded(safeText(strings.ReplaceAll(m.notice, "\n", " ")), 160), 1)
	}
	view := tea.NewView(lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(content))
	view.AltScreen = true
	return view
}
