package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

type approvalModal struct {
	requestID, hash, reason string
	denying, pending, sent  bool
	target                  string
	argsTruncated           bool
	previewTruncated        bool
	offset                  int // Wrapped display rows, independent of the transcript.
}
type approvalMsg struct {
	epoch           uint64
	requestID, hash string
	err             error
}

func (m *Model) syncApproval(p *workflows.PendingApproval) {
	if p == nil {
		m.approval = approvalModal{}
		return
	}
	if m.approval.requestID != p.RequestID || m.approval.hash != p.ActionHash {
		m.approval = approvalModal{requestID: p.RequestID, hash: p.ActionHash}
	}
	if p.Decision != nil {
		m.approval.sent = true
	}
	// Parse the raw JSON before displayState bounds it: large write content
	// commonly precedes the path, and truncated JSON cannot be decoded.
	var args struct {
		Path    string `json:"path"`
		Cmd     string `json:"cmd"`
		Workdir string `json:"workdir"`
	}
	target := "Target: see arguments"
	if err := json.Unmarshal(p.Call.Args, &args); err != nil {
		target = "Target: unavailable (invalid JSON)"
	} else if args.Path != "" {
		target = "Path: " + args.Path
	} else if args.Cmd != "" {
		target = "Command: " + args.Cmd
		if args.Workdir != "" {
			target += " · in " + args.Workdir
		}
	}
	m.approval.target = approvalBound(singleLine(target), 512, "target")
	m.approval.argsTruncated = len(safeText(string(p.Call.Args))) > maxText
	m.approval.previewTruncated = len(safeText(p.Preview)) > maxText
}

func (m *Model) approvalView() string {
	p := m.state.PendingApproval
	if p == nil {
		return ""
	}
	remaining := time.Until(p.ExpiresAt)
	countdown := "expired"
	if remaining > 0 {
		countdown = fmt.Sprintf("%ds remaining", int(remaining.Seconds())+1)
	}
	return singleLine("Approval pending: " + p.RequestID + " · " + p.Call.Name + " · " + countdown)
}

func approvalBound(s string, limit int, label string) string {
	if len(s) > limit {
		return bounded(s, limit) + "\n[" + label + " truncated]"
	}
	return s
}

// Only bounded, sanitized display data belongs in the pager. Decision identity
// remains the raw snapshot captured by syncApproval, never this rendered text.
func (m *Model) approvalOverlay() string {
	p := m.state.PendingApproval
	if p == nil {
		return ""
	}
	preview := safeText(p.Preview)
	if preview == "" {
		preview = "(no preview supplied)"
	}
	if m.approval.previewTruncated {
		preview += "\n[preview truncated]"
	}
	args := safeText(string(p.Call.Args))
	if m.approval.argsTruncated {
		args += "\n[arguments truncated]"
	}
	body := "Action: " + singleLine(p.Call.Name) + "\n" + m.approval.target +
		"\nRequest: " + singleLine(p.RequestID) + "\nPreview:\n" + preview + "\nArguments:\n" + args
	if m.approval.denying {
		body += "\nDenial reason:\n" + m.approval.reason
	}
	return body
}

// Use the whole screen while blocked, rather than the often tiny transcript
// pane. Six fixed rows reserve status, navigation, decisions and stop/quit.
func (m *Model) approvalPage() (lines []string, start, height int) {
	lines = strings.Split(ansi.Hardwrap(m.approvalOverlay(), max(1, m.width), true), "\n")
	height = max(1, m.height-6)
	start = max(0, min(m.approval.offset, max(0, len(lines)-height)))
	return
}

func (m *Model) approvalScreen() string {
	lines, start, height := m.approvalPage()
	end := min(len(lines), start+height)
	body := append([]string(nil), lines[start:end]...)
	for len(body) < height {
		body = append(body, "")
	}
	remaining := "expired"
	if d := time.Until(m.state.PendingApproval.ExpiresAt); d > 0 {
		remaining = fmt.Sprintf("%ds remaining", int(d.Seconds())+1)
	}
	controls, status := "a approve · d deny", singleLine(m.notice)
	if m.approval.denying {
		controls = "Denial reason: Enter sends"
		// Keep the input tail visible; the full bounded reason is also paged.
		status = "> " + ansi.TruncateLeft(m.approval.reason, max(0, lipgloss.Width(m.approval.reason)-(m.width-4)), "…")
		if strings.TrimSpace(m.approval.reason) == "" {
			controls = "Denial reason required; Enter sends"
		}
		if m.notice != "" && m.notice != "A denial reason is required" {
			status = singleLine(m.notice)
		}
	}
	if m.approval.pending {
		controls = "Recording decision…"
	} else if m.approval.sent {
		controls = "Decision recorded; waiting for state"
	}
	position := fmt.Sprintf("Lines %d-%d/%d", start+1, end, len(lines))
	if start > 0 {
		position += " · more above"
	}
	if end < len(lines) {
		position += " · more below"
	}
	rows := append([]string{"Approval blocked · " + remaining}, body...)
	rows = append(rows, position, "PgUp/PgDn Up/Down Home/End", controls, status, "Esc stop · Ctrl+C quit")
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], m.width, "…")
	}
	return lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(strings.Join(rows, "\n"))
}

func (m *Model) approvalKey(key tea.KeyPressMsg) tea.Cmd {
	// Navigation stays available during reason entry and in-flight/completed
	// decisions. Esc/Ctrl+C are still handled first by Model.Update.
	lines, start, height := m.approvalPage()
	switch key.Keystroke() {
	case "pgup":
		m.approval.offset = max(0, start-height)
	case "pgdown":
		m.approval.offset = min(max(0, len(lines)-height), start+height)
	case "up":
		m.approval.offset = max(0, start-1)
	case "down":
		m.approval.offset = min(max(0, len(lines)-height), start+1)
	case "home":
		m.approval.offset = 0
	case "end":
		m.approval.offset = max(0, len(lines)-height)
	default:
		return m.approvalDecisionKey(key)
	}
	return nil
}

func (m *Model) approvalDecisionKey(key tea.KeyPressMsg) tea.Cmd {
	if m.approval.pending || m.approval.sent {
		return nil
	}
	if m.approval.denying {
		switch key.Keystroke() {
		case "enter":
			if strings.TrimSpace(m.approval.reason) == "" {
				m.notice = "A denial reason is required"
				return nil
			}
			return m.decideCmd(false)
		case "backspace":
			_, n := utf8.DecodeLastRuneInString(m.approval.reason)
			m.approval.reason = m.approval.reason[:len(m.approval.reason)-n]
		default:
			text := key.Text
			if text == "" && key.Mod == 0 && key.Code >= 32 && key.Code < 127 {
				text = string(key.Code)
			}
			m.approval.reason = bounded(m.approval.reason+singleLine(text), 4096)
		}
		return nil
	}
	switch key.Keystroke() {
	case "a":
		return m.decideCmd(true)
	case "d":
		m.approval.denying = true
	}
	return nil
}

func (m *Model) decideCmd(approved bool) tea.Cmd {
	p := m.state.PendingApproval
	if p == nil || !time.Now().Before(p.ExpiresAt) {
		m.notice = "Approval expired; refresh pending state"
		return nil
	}
	epoch, id, parent, backend := m.epoch, m.opts.SessionID, m.ctx, m.backend
	d := workflows.DecisionInput{RequestID: m.approval.requestID, ActionHash: m.approval.hash, Approved: approved, Reason: m.approval.reason}
	m.approval.pending = true
	return func() tea.Msg {
		result := approvalMsg{epoch: epoch, requestID: d.RequestID, hash: d.ActionHash}
		ctx, cancel := context.WithTimeout(parent, requestTimeout)
		defer cancel()
		// This is a local display identity, not authentication. Authorization belongs
		// at the caller/service boundary.
		d.User = strings.TrimSpace(os.Getenv("USER"))
		if d.User == "" {
			result.err = fmt.Errorf("approval user is required ($USER)")
			return result
		}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		state, err := backend.Query(ctx, id)
		if err != nil {
			result.err = err
			return result
		}
		current := state.PendingApproval
		if current == nil || current.RequestID != d.RequestID || current.ActionHash != d.ActionHash || current.Decision != nil || !time.Now().Before(current.ExpiresAt) {
			result.err = fmt.Errorf("stale approval; refresh pending state")
			return result
		}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		result.err = backend.Decide(ctx, id, d)
		return result
	}
}
