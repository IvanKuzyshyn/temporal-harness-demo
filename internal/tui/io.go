package tui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type pollMsg struct{ epoch uint64 }
type tailMsg struct {
	epoch     uint64
	events    []events.Event
	offset    int64
	at        time.Time
	err       error
	truncated bool
}
type snapshotMsg struct {
	epoch, query uint64
	state        workflows.State
	at           time.Time
	err          error
}
type operationMsg struct {
	epoch         uint64
	label, detail string
	err           error
}
type newSessionMsg struct {
	epoch uint64
	id    string
	err   error
}

func (m *Model) pollCmd() tea.Cmd {
	epoch := m.epoch
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollMsg{epoch} })
}

func (m *Model) tailCmd() tea.Cmd {
	epoch, id, dir, offset, ctx := m.epoch, m.opts.SessionID, m.opts.DataDir, m.offset, m.ctx
	return func() tea.Msg {
		result := tailMsg{epoch: epoch, offset: offset, at: time.Now()}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		path, err := events.NewStore(dir).Path(id)
		if err == nil {
			result.events, result.offset, err = events.Tail(path, offset)
			for i, e := range result.events {
				result.events[i] = compactEvent(e)
			}
		}
		result.err = err
		result.truncated = errors.Is(err, events.ErrTruncated)
		return result
	}
}

func (m *Model) queryCmd() tea.Cmd {
	epoch, query, id, backend, parent := m.epoch, m.queryID, m.opts.SessionID, m.backend, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, requestTimeout)
		defer cancel()
		result := snapshotMsg{epoch: epoch, query: query}
		if backend == nil {
			result.err = fmt.Errorf("no backend configured")
		} else {
			result.state, result.err = backend.Query(ctx, id)
		}
		result.at = time.Now()
		return result
	}
}

func requestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (m *Model) newSessionCmd() tea.Cmd {
	epoch := m.epoch
	return func() tea.Msg {
		id, err := requestID()
		return newSessionMsg{epoch: epoch, id: "session-" + id, err: err}
	}
}

func (m *Model) submitCmd(text, mode string, demo bool) tea.Cmd {
	epoch, id, backend, parent, in := m.epoch, m.opts.SessionID, m.backend, m.ctx, m.opts.Input
	if demo {
		in.Scenario = "demo"
		// Existing sessions ignore Input overrides, so the message must opt in too.
		text = demoPrompt
	}
	return func() tea.Msg {
		result := operationMsg{epoch: epoch, label: "Submit"}
		request, err := requestID()
		if err != nil {
			result.err = err
			return result
		}
		ctx, cancel := context.WithTimeout(parent, requestTimeout)
		defer cancel()
		if backend == nil {
			result.err = fmt.Errorf("no backend configured")
			return result
		}
		receipt, err := backend.Submit(ctx, id, in, workflows.MessageInput{Text: text, WhenBusy: mode, RequestID: request})
		result.err = err
		result.detail = receipt.Disposition
		return result
	}
}

func (m *Model) actionCmd(action string) tea.Cmd {
	epoch, id, backend, parent := m.epoch, m.opts.SessionID, m.backend, m.ctx
	return func() tea.Msg {
		result := operationMsg{epoch: epoch, label: action, detail: "requested"}
		ctx, cancel := context.WithTimeout(parent, requestTimeout)
		defer cancel()
		if backend == nil {
			result.err = fmt.Errorf("no backend configured")
			return result
		}
		switch action {
		case "stop":
			result.err = backend.Stop(ctx, id)
		case "continue":
			result.err = backend.Continue(ctx, id)
		case "sessions":
			var ids []string
			ids, result.err = backend.Sessions(ctx)
			result.detail = "Use /sessions <id> to reconnect:"
			for _, id := range ids[:min(len(ids), maxSessions)] {
				result.detail += "\n  " + safeText(id)
			}
		}
		return result
	}
}

// compactEvent keeps replay buffers independent of large tool arguments and
// unused payloads. The on-disk stream remains the complete diagnostic record.
func compactEvent(e events.Event) events.Event {
	e.TS, e.RunID = "", ""
	e.WorkflowID = bounded(safeText(e.WorkflowID), 256)
	e.ParentWorkflowID = bounded(safeText(e.ParentWorkflowID), 256)
	e.Type = events.Type(bounded(safeText(string(e.Type)), 128))
	e.Pattern = events.Pattern(bounded(safeText(string(e.Pattern)), 128))
	var payload any
	switch e.Type {
	case events.MessageStart, events.MessageUpdate:
		var d events.MessageData
		if json.Unmarshal(e.Data, &d) == nil {
			// References are opaque identities, never display text. Hash rather
			// than truncate/sanitize so long refs with shared prefixes stay distinct.
			ref := ""
			if d.MessageRef != "" {
				sum := sha256.Sum256([]byte(d.MessageRef))
				ref = hex.EncodeToString(sum[:])
			}
			message := events.MessageData{Role: bounded(safeText(d.Role), 32), MessageRef: ref}
			if e.Type == events.MessageUpdate {
				message.Delta = bounded(safeText(d.Delta), maxText)
			}
			payload = message
		}
	case events.ToolStart, events.ToolUpdate, events.ToolEnd:
		var d events.ToolData
		if json.Unmarshal(e.Data, &d) == nil {
			payload = events.ToolData{ToolCallID: bounded(safeText(d.ToolCallID), 256), Name: bounded(safeText(d.Name), 256), Chunk: bounded(safeText(d.Chunk), maxText), Summary: bounded(safeText(d.Summary), maxText), IsError: d.IsError, Interrupted: d.Interrupted}
		}
	case events.ChildStarted, events.ChildCompleted:
		var d events.ChildData
		if json.Unmarshal(e.Data, &d) == nil {
			payload = events.ChildData{ChildWorkflowID: bounded(safeText(d.ChildWorkflowID), 256), Task: bounded(safeText(d.Task), maxText)}
		}
	}
	e.Data, _ = json.Marshal(payload)
	return e
}

func needsSnapshot(es []events.Event) bool {
	for _, e := range es {
		switch e.Type {
		case events.AgentEnd, events.AgentSettled, events.RunFailed, events.ApprovalRequested, events.ApprovalResolved:
			return true
		}
	}
	return false
}
