// Package tui provides a terminal-independent Bubble Tea frontend. The backend
// owns durable session state; the event stream is only a live display aid.
package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// Backend is implemented by the session client. Calls execute only in tea.Cmd.
type Backend interface {
	Submit(context.Context, string, workflows.Input, workflows.MessageInput) (workflows.MessageReceipt, error)
	Query(context.Context, string) (workflows.State, error)
	Stop(context.Context, string) error
	Continue(context.Context, string) error
	Decide(context.Context, string, workflows.DecisionInput) error
	Sessions(context.Context) ([]string, error)
}

// Options describes the initial session. Empty SessionID generates a new ID in
// Init. Demo offers a banner; Autoplay explicitly submits the demo at startup.
type Options struct {
	// Context bounds the model lifetime; nil uses context.Background().
	Context                   context.Context
	SessionID, Model, DataDir string
	Input                     workflows.Input
	Demo, Autoplay            bool
}

const (
	pollInterval     = 150 * time.Millisecond
	snapshotInterval = 2 * time.Second
	requestTimeout   = 10 * time.Second
	maxEntries       = 128
	maxText          = 4096
	maxAnswer        = 64 * 1024
	maxSessions      = 32
	maxReplayEvents  = 512
	maxPromptRunes   = 16 * 1024
)

type streamEntry struct {
	label, text                              string
	messageRef, workflowID, parentWorkflowID string
}

// Model implements tea.Model using pointer semantics. Do not copy a Model or
// call Update concurrently. Construct it with New.
type Model struct {
	backend                         Backend
	opts                            Options
	lifetime                        context.Context
	close                           context.CancelFunc
	ctx                             context.Context
	cancel                          context.CancelFunc
	epoch, queryID                  uint64
	started, queryPending, quitting bool
	reconnect                       *events.Reconnect
	pending                         []events.Event
	lastSnapshot                    time.Time
	lastSeq                         uint64
	displaySeq                      uint64
	recent                          []events.Event
	newMessage                      bool
	offset                          int64
	offsets                         map[string]int64
	sessionOrder                    []string
	state                           workflows.State
	transcript, patterns, commands  []string
	stream                          []streamEntry
	notice                          string
	showPatterns, collapseTools     bool
	width, height                   int
	prompt                          textarea.Model
	viewport                        viewport.Model
	spinner                         spinner.Model
}

var _ tea.Model = (*Model)(nil)

// New constructs a model without performing network or file operations.
func New(backend Backend, opts Options) *Model {
	if opts.Model == "" {
		opts.Model = opts.Input.Model
	}
	if opts.DataDir == "" {
		opts.DataDir = opts.Input.DataDir
	}
	opts.Input.Model = opts.Model
	opts.Input.DataDir = opts.DataDir
	parent := opts.Context
	if parent == nil {
		parent = context.Background()
	}
	lifetime, closeModel := context.WithCancel(parent)
	ctx, cancel := context.WithCancel(lifetime)
	p := textarea.New()
	p.Prompt = "> "
	p.Placeholder = "Message or /help"
	p.ShowLineNumbers = false
	p.CharLimit = 0 // We enforce retained runes, not Bubbles' display-cell limit.
	p.KeyMap.Paste.SetEnabled(false)
	p.MaxWidth = 0
	p.MaxHeight = 64
	p.SetHeight(3)
	p.Focus()
	v := viewport.New()
	v.SoftWrap = true
	v.FillHeight = true
	m := &Model{backend: backend, opts: opts, lifetime: lifetime, close: closeModel, ctx: ctx, cancel: cancel, epoch: 1, reconnect: events.NewReconnect(), offsets: make(map[string]int64), prompt: p, viewport: v, spinner: spinner.New()}
	m.resize(80, 24)
	return m
}

// Close cancels all backend-operation contexts, including future sessions.
// It is idempotent and safe to call concurrently with Update. It does not wait
// for commands to return; backends must honor context cancellation.
func (m *Model) Close() { m.close() }

func (m *Model) Init() tea.Cmd {
	if m.opts.SessionID == "" {
		return tea.Batch(m.newSessionCmd(), m.spinner.Tick, textarea.Blink)
	}
	return tea.Batch(m.tailCmd(), m.spinner.Tick, textarea.Blink)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "ctrl+c":
			m.quitting = true
			m.Close()
			return m, tea.Quit
		case "esc":
			return m, m.actionCmd("stop")
		case "enter":
			return m, m.submitPrompt(false)
		case "alt+enter":
			return m, m.submitPrompt(true)
		case "ctrl+v":
			return m, m.clipboardCmd(readClipboard)
		case "shift+enter", "ctrl+j":
			m.insertPrompt("\n")
			return m, nil
		case "tab":
			m.showPatterns = !m.showPatterns
			m.refreshViewport(true)
			return m, nil
		case "ctrl+t":
			m.collapseTools = !m.collapseTools
			return m, nil
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		}
	case pollMsg:
		if msg.epoch != m.epoch {
			return m, nil
		}
		return m, m.tailCmd()
	case tailMsg:
		if msg.epoch != m.epoch {
			return m, nil
		}
		m.offset = msg.offset
		var cmds []tea.Cmd
		cmds = append(cmds, m.pollCmd())
		if msg.err != nil {
			m.notice = "Stream: " + bounded(safeText(msg.err.Error()), maxText)
			if msg.truncated {
				m.offset = 0
			}
		}
		if !m.started {
			m.started = true
			cmds = append(cmds, m.beginQuery())
			if m.opts.Autoplay {
				m.opts.Autoplay = false
				cmds = append(cmds, m.submitCmd(demoPrompt, "followUp", true))
			}
		}
		if m.reconnect != nil {
			if err := m.bufferEvents(msg.events); err != nil {
				m.notice = "Stream buffer full; refreshing snapshot"
				cmds = append(cmds, m.restartQuery())
			}
		} else {
			for i, e := range msg.events {
				if e.Seq <= m.lastSeq {
					continue
				}
				if e.Seq != m.lastSeq+1 {
					m.notice = "Stream gap; refreshing snapshot"
					cmds = append(cmds, m.beginQuery())
					if err := m.bufferEvents(msg.events[i:]); err != nil {
						cmds = append(cmds, m.restartQuery())
					}
					break
				}
				m.applyEvent(e)
			}
		}
		if !m.queryPending && (msg.err != nil || msg.at.Sub(m.lastSnapshot) >= snapshotInterval || needsSnapshot(msg.events)) {
			cmds = append(cmds, m.beginQuery())
		}
		m.refreshViewport(false)
		return m, tea.Batch(cmds...)
	case snapshotMsg:
		if msg.epoch != m.epoch || msg.query != m.queryID {
			return m, nil
		}
		m.queryPending = false
		m.lastSnapshot = msg.at
		if msg.err != nil {
			m.notice = "Snapshot: " + bounded(safeText(msg.err.Error()), maxText)
			// Keep buffering until the next timed retry. A missing workflow is normal
			// for a new session, which will be created by the first Submit.
			return m, nil
		}
		if strings.HasPrefix(m.notice, "Stream") || strings.HasPrefix(m.notice, "Snapshot:") {
			m.notice = ""
		}
		m.state = displayState(msg.state)
		m.lastSeq = msg.state.LastSeq
		m.stream = nil
		m.recent = nil
		m.newMessage = false
		if m.reconnect != nil {
			pending, err := m.reconnect.Resolve(m.lastSeq)
			m.reconnect = nil
			if err != nil {
				m.reconnect = events.NewReconnect()
				_ = m.reconnect.Buffer(m.pending)
				m.notice = "Stream gap; refreshing snapshot"
				m.refreshViewport(false)
				return m, m.beginQuery()
			}
			m.pending = nil
			for _, e := range pending {
				m.applyEvent(e)
			}
		}
		m.refreshViewport(false)
		return m, nil
	case operationMsg:
		if msg.epoch != m.epoch {
			return m, nil
		}
		if msg.err != nil {
			m.notice = msg.label + ": " + safeText(msg.err.Error())
		} else {
			m.notice = msg.label + ": " + safeText(msg.detail)
		}
		m.notice = bounded(m.notice, maxText)
		m.commands = appendBounded(m.commands, m.notice)
		if msg.label == "sessions" {
			m.transcript = appendBounded(m.transcript, m.notice)
			m.refreshViewport(true)
		}
		// The periodic snapshot also covers updates that complete after this query.
		if m.started && !m.queryPending {
			return m, m.beginQuery()
		}
		return m, nil
	case newSessionMsg:
		if msg.epoch != m.epoch {
			return m, nil
		}
		if msg.err != nil {
			m.notice = "New session: " + bounded(safeText(msg.err.Error()), maxText)
			return m, nil
		}
		return m, m.switchSession(msg.id)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case clipboardMsg:
		if msg.epoch != m.epoch {
			return m, nil
		}
		if msg.err != nil {
			m.notice = "Clipboard: " + bounded(safeText(msg.err.Error()), maxText)
			return m, nil
		}
		m.insertPrompt(msg.text)
		return m, nil
	case tea.PasteMsg:
		m.insertPrompt(msg.Content)
		return m, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		key.Text = m.promptBudget(key.Text)
		msg = key
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	m.capPrompt()
	return m, cmd
}

func (m *Model) beginQuery() tea.Cmd {
	if m.queryPending {
		return nil
	}
	if m.reconnect == nil {
		m.reconnect = events.NewReconnect()
		// State.LastSeq may lag the live stream while an activity is running.
		// Keep newer already-rendered deltas through periodic snapshots too.
		_ = m.bufferEvents(m.recent)
	}
	m.queryID++
	m.queryPending = true
	return m.queryCmd()
}

func (m *Model) restartQuery() tea.Cmd {
	// Overflow discarded data: the last-read offset is no longer replay-safe.
	m.offset = 0
	m.pending = nil
	m.reconnect = events.NewReconnect()
	m.queryPending = false
	return m.beginQuery()
}

func (m *Model) switchSession(id string) tea.Cmd {
	if _, ok := m.offsets[m.opts.SessionID]; !ok && m.opts.SessionID != "" {
		m.sessionOrder = append(m.sessionOrder, m.opts.SessionID)
	}
	if m.opts.SessionID != "" {
		// Without caching the full pending replay, only the file origin is safe.
		m.offsets[m.opts.SessionID] = 0
	}
	if len(m.sessionOrder) > maxSessions {
		delete(m.offsets, m.sessionOrder[0])
		m.sessionOrder = m.sessionOrder[1:]
	}
	m.cancel()
	m.ctx, m.cancel = context.WithCancel(m.lifetime)
	m.epoch++
	m.opts.SessionID = id
	m.offset = m.offsets[id]
	m.lastSeq = 0
	m.displaySeq = 0
	m.recent = nil
	m.pending = nil
	m.newMessage = false
	m.state = workflows.State{}
	m.started = false
	m.queryPending = false
	m.reconnect = events.NewReconnect()
	m.lastSnapshot = time.Time{}
	m.transcript = nil
	m.stream = nil
	m.patterns = nil
	m.commands = nil
	m.notice = "Connecting"
	m.prompt.Reset()
	m.refreshViewport(true)
	return m.tailCmd()
}
