package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
)

const demoPrompt = "/demo"
const helpText = "Enter: follow-up | Alt+Enter or /steer <text>: steer | Shift+Enter (Ctrl+J): newline\nEsc: stop | Ctrl+C: detach | Tab: journal/patterns | PgUp/PgDn or wheel: scroll | End: follow latest\nCtrl+Up/Down: select record | Ctrl+O: detail | Ctrl+T: collapse tools\nCtrl+L /log: captured command log | Ctrl+Left/Right /logprev /lognext: byte pages\n/older: previous 64 records | /latest: newest/retry | /detail <record number> | /journal\n/metadata: full original configuration | /continue | /new [id] | /sessions [id] | /help | /demo | /explore <task> | /run <command>"

func (m *Model) submitPrompt(steer bool) tea.Cmd {
	text := strings.TrimSpace(m.prompt.Value())
	if text == "" {
		return nil
	}
	fields := strings.Fields(text)
	name := fields[0]
	rest := strings.TrimSpace(strings.TrimPrefix(text, name))
	demo := false
	switch name {
	case "/help":
		m.history.detailID, m.selectedID = "", ""
		m.log = logState{}
		m.showPatterns = false
		m.transcript = appendBounded(m.transcript, helpText)
		m.prompt.Reset()
		m.refreshViewport(true)
		return nil
	case "/metadata":
		m.history.detailID, m.selectedID = "", ""
		m.log = logState{}
		m.showPatterns = false
		for _, line := range strings.Split("Session: "+singleLine(m.opts.SessionID)+"\n"+m.sessionConfiguration()+"\nDataDir: "+singleLine(m.history.dir), "\n") {
			m.transcript = appendBounded(m.transcript, line)
		}
		m.prompt.Reset()
		m.refreshViewport(true)
		return nil
	case "/log", "/lognext", "/logprev":
		m.prompt.Reset()
		if name == "/lognext" {
			return m.pageLog(1)
		}
		if name == "/logprev" {
			return m.pageLog(-1)
		}
		return m.openSelectedLog()
	case "/detail":
		n, err := strconv.Atoi(rest)
		i := n - m.history.page.Before - 1
		if err != nil || i < 0 || i >= len(m.history.page.Entries) {
			m.notice = "Use /detail <record number> from the loaded history window"
			return nil
		}
		m.browse()
		m.showPatterns = false
		m.history.detailID = m.history.page.Entries[i].ID
		m.selectedID = m.history.detailID
		m.log = logState{}
		m.prompt.Reset()
		m.rebuildJournal()
		m.viewport.GotoTop()
		return nil
	case "/journal":
		m.showPatterns = false
		m.history.detailID = ""
		m.log = logState{}
		m.prompt.Reset()
		m.rebuildJournal()
		return nil
	case "/older":
		m.prompt.Reset()
		if !m.history.loaded || !m.history.page.HasMore {
			m.notice = "No older page available"
			return nil
		}
		return m.loadHistory(m.history.page.Before)
	case "/latest":
		m.prompt.Reset()
		return m.jumpLatest()
	case "/continue":
		m.prompt.Reset()
		return m.actionCmd("continue")
	case "/new":
		if rest == "" {
			m.prompt.Reset()
			return m.newSessionCmd()
		}
		return m.selectSession(rest)
	case "/sessions":
		if rest != "" {
			return m.selectSession(rest)
		}
		m.prompt.Reset()
		return m.actionCmd("sessions")
	case "/steer":
		text = rest
		steer = true
	case "/demo":
		text = demoPrompt
		demo = true
	case "/explore", "/run": // Forward unchanged; interpretation belongs to workflows.
	default:
		if strings.HasPrefix(name, "/") {
			m.notice = "Unknown command; use /help"
			return nil
		}
	}
	if strings.TrimSpace(text) == "" {
		m.notice = "Enter a non-empty message"
		return nil
	}
	if m.opts.SessionID == "" {
		m.notice = "Session is initializing; try again"
		return nil
	}
	mode := "followUp"
	if steer {
		mode = "steer"
	}
	// Submit receipts carry no durable message ID. Do not create an optimistic
	// conversation row that cannot later be reconciled by identity.
	m.notice = "Submitting " + mode + "; awaiting durable history"
	m.prompt.Reset()
	m.refreshViewport(true)
	return m.submitCmd(text, mode, demo)
}

func (m *Model) selectSession(id string) tea.Cmd {
	// Path validates the ID but performs no file I/O.
	if _, err := events.NewStore(m.opts.DataDir).Path(id); err != nil {
		m.notice = "Invalid session: " + safeText(err.Error())
		return nil
	}
	return m.switchSession(id)
}
