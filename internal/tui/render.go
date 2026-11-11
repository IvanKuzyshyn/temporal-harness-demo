package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
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

// Enum classification must not be invented by removing controls or eliding an
// identity. An altered value stays visibly unknown rather than becoming a known
// activity/mode after display sanitation.
func displayEnum(raw string, limit int) string {
	text := singleLine(raw)
	if text != raw || len(text) > limit {
		return bounded("unknown ("+text+")", limit)
	}
	return text
}

// Retain only bounded display data, not potentially large workflow inboxes,
// tool arguments, or child collections returned by the backend.
func displayState(s workflows.State) workflows.State {
	out := workflows.State{Status: displayEnum(s.Status, 128), Turn: s.Turn, MaxTurns: s.MaxTurns, LastSeq: s.LastSeq, Answer: bounded(safeText(s.Answer), maxAnswer)}
	out.Phase, out.Mode = displayEnum(s.Phase, 64), displayEnum(s.Mode, 64)
	out.PhaseStartedAt, out.RunStartedAt, out.RunFinishedAt = s.PhaseStartedAt, s.RunStartedAt, s.RunFinishedAt
	for _, tool := range s.ActiveTools[:min(32, len(s.ActiveTools))] {
		out.ActiveTools = append(out.ActiveTools, llm.ToolCall{Name: bounded(singleLine(tool.Name), 128)})
	}
	for _, child := range s.Children[:min(64, len(s.Children))] {
		out.Children = append(out.Children, workflows.ChildState{Status: displayEnum(child.Status, 32)})
	}
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

func (m *Model) applyEvent(e streamEvent) {
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
	if e.Type == events.Heartbeat && e.workflowKey == optionalIdentity(m.opts.SessionID) && e.parentKey == "" {
		if at, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && at.After(m.heartbeat) {
			m.heartbeat = at
		}
	}
	if newLog && !m.followLive {
		m.unread = min(m.unread+1, 999999)
	}
	prefix := ""
	if e.parentKey != "" || (e.workflowKey != "" && e.workflowKey != optionalIdentity(m.opts.SessionID)) {
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
		label := prefix + "Assistant (stream draft, best-effort): "
		if d.MessageRef != "" && (d.Role == "assistant" || d.Role == "") {
			candidate := streamEntry{messageRef: d.MessageRef, workflowKey: e.workflowKey, parentKey: e.parentKey}
			if m.persistedStream(candidate) {
				m.retireStream(d.MessageRef)
				m.newMessage = true
				return
			}
			// Attempt identity lives on bounded entries; completed identities have
			// a separate bounded replay horizon that survives page replacement.
			index := -1
			for i := range m.stream {
				entry := &m.stream[i]
				if entry.messageRef == d.MessageRef && entry.workflowKey == e.workflowKey && entry.parentKey == e.parentKey {
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
				m.stream = append(m.stream, streamEntry{label: label, messageRef: d.MessageRef, workflowKey: e.workflowKey, parentKey: e.parentKey})
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
		if m.newMessage || len(m.stream) == 0 || m.stream[len(m.stream)-1].label != label ||
			m.stream[len(m.stream)-1].workflowKey != e.workflowKey || m.stream[len(m.stream)-1].parentKey != e.parentKey {
			if len(m.stream) >= maxEntries {
				copy(m.stream, m.stream[1:])
				m.stream = m.stream[:maxEntries-1]
			}
			m.stream = append(m.stream, streamEntry{label: label, workflowKey: e.workflowKey, parentKey: e.parentKey})
		}
		m.newMessage = false
		last := &m.stream[len(m.stream)-1]
		last.text = bounded(last.text+safeText(d.Delta), maxAnswer)
	case events.ToolStart, events.ToolUpdate, events.ToolEnd:
		var d events.ToolData
		if json.Unmarshal(e.Data, &d) != nil {
			return
		}
		line := prefix + string(e.Type) + " " + d.Name + " " + d.ToolCallID
		if e.Type == events.ToolStart || e.Type == events.ToolUpdate {
			line += " [running; best-effort]"
		}
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
		if newLog {
			m.commands = appendBounded(m.commands, line)
		}
		m.appendLiveEntry(streamEntry{tool: true, label: "Live tool event (unverified): ", text: bounded(safeText(line), maxText)})
	case events.ChildStarted, events.ChildCompleted:
		var d events.ChildData
		if json.Unmarshal(e.Data, &d) == nil {
			m.appendLiveEntry(streamEntry{label: "Live child progress (best-effort): ", text: bounded(safeText(d.ChildWorkflowID+": "+string(e.Type)+" "+d.Task), maxText)})
		}
	}
}

func (m *Model) refreshViewport(forceBottom bool) {
	if forceBottom {
		m.followLive = true
		m.unread = 0
	}
	if m.followLive || m.journalParts == nil {
		m.rebuildJournal()
	}
	if m.followLive {
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
	if m.configMismatch {
		text += " [mismatch: client selection not applied]"
	}
	return text
}
