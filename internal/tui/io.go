package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

// streamEvent is a TUI-only projection. Event workflow IDs are display labels;
// opaque keys preserve identity across sanitization, truncation and replay.
// Nothing is added to or repurposed in the on-disk event protocol.
type streamEvent struct {
	events.Event
	workflowKey, parentKey string
}

type pollMsg struct{ epoch uint64 }
type tailMsg struct {
	epoch     uint64
	dir       string
	events    []streamEvent
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
	// Once Query supplies original metadata, never tail a different client's
	// directory. Legacy sessions may still use the optional display stream;
	// transcript reads never use that fallback.
	authoritative := m.state.Session != nil
	if authoritative {
		dir = m.history.dir
	}
	return func() tea.Msg {
		result := tailMsg{epoch: epoch, dir: dir, offset: offset, at: time.Now()}
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		if authoritative && !filepath.IsAbs(dir) {
			return result
		}
		path, err := events.NewStore(dir).Path(id)
		if err == nil {
			var raw []events.Event
			raw, result.offset, err = events.Tail(path, offset)
			for _, e := range raw {
				result.events = append(result.events, compactEvent(e))
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
func compactEvent(e events.Event) streamEvent {
	workflowKey, parentKey := optionalIdentity(e.WorkflowID), optionalIdentity(e.ParentWorkflowID)
	e.RunID = ""
	if at, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
		e.TS = at.UTC().Format(time.RFC3339Nano)
	} else {
		e.TS = ""
	}
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
				ref = messageRefIdentity(d.MessageRef)
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
	return streamEvent{Event: e, workflowKey: workflowKey, parentKey: parentKey}
}

func optionalIdentity(raw string) string {
	if raw == "" {
		return ""
	}
	return messageRefIdentity(raw)
}

func needsSnapshot(es []streamEvent) bool {
	for _, e := range es {
		switch e.Type {
		case events.AgentEnd, events.AgentSettled, events.RunFailed, events.ApprovalRequested, events.ApprovalResolved:
			return true
		}
	}
	return false
}
