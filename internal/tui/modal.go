package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type approvalModal struct {
	requestID, hash, reason string
	denying, pending, sent  bool
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

// Overlay occupies the transcript region, not extra layout rows: footer and
// Esc/Ctrl+C remain visible even with a multiline preview on small terminals.
func (m *Model) approvalOverlay() string {
	p := m.state.PendingApproval
	if p == nil {
		return ""
	}
	controls := "a approve · d deny · Esc stop · Ctrl+C quit"
	if m.approval.denying {
		controls = "Denial reason (Enter sends): " + m.approval.reason
	}
	if m.approval.pending {
		controls = "Recording decision… · Esc stop · Ctrl+C quit"
	}
	if m.approval.sent {
		controls = "Decision recorded; waiting for authoritative state"
	}
	return m.approvalView() + "\n" + controls + "\n" + bounded(singleLine(p.Call.Name+" "+string(p.Call.Args)), maxText) + "\nPreview:\n" + bounded(safeText(p.Preview), maxText)
}

func (m *Model) approvalKey(key tea.KeyPressMsg) tea.Cmd {
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
