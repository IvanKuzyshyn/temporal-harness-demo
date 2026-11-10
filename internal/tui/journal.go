package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
)

const maxJournalText = 512 << 10

// Tool results are associated only inside this contiguous window, scoped to the
// immediately preceding assistant message. Never use a global call-ID index.
type journalEntry struct {
	record   activities.TranscriptRecord
	parentID string
}

func journalEntries(records []activities.TranscriptRecord) []journalEntry {
	rows := make([]journalEntry, 0, len(records))
	pending := map[string]string{}
	names := map[string]string{}
	for _, record := range records {
		msg := record.Message
		row := journalEntry{record: record}
		if msg.Role != "tool" {
			clear(pending)
			clear(names)
		}
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				pending[call.ID] = record.ID
				names[call.ID] = call.Name
			}
		}
		if msg.Role == "tool" && (msg.ToolName == "" || names[msg.ToolCallID] == msg.ToolName) {
			row.parentID = pending[msg.ToolCallID]
			delete(pending, msg.ToolCallID)
			delete(names, msg.ToolCallID)
		}
		rows = append(rows, row)
	}
	return rows
}
func messageRefIdentity(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:])
}
func (m *Model) appendLiveEntry(entry streamEntry) {
	if len(m.stream) >= maxEntries {
		copy(m.stream, m.stream[1:])
		m.stream = m.stream[:maxEntries-1]
	}
	m.stream = append(m.stream, entry)
	m.newMessage = true
}
func (m *Model) persistedStream(entry streamEntry) bool {
	if entry.messageRef == "" || entry.parentKey != "" || (entry.workflowKey != "" && entry.workflowKey != optionalIdentity(m.opts.SessionID)) {
		return false
	}
	for _, ref := range m.history.retired {
		if entry.messageRef == ref {
			return true
		}
	}
	ref := activities.TranscriptRef(m.opts.SessionID)
	for _, r := range m.history.page.Entries {
		if entry.messageRef == messageRefIdentity(ref+"#"+r.ID) {
			return true
		}
	}
	return false
}

// Retirement outlives page replacement and successful snapshots. Keep a bounded
// replay horizon, not an ever-growing index of every historical message.
func (m *Model) retireStream(ref string) {
	for _, old := range m.history.retired {
		if old == ref {
			return
		}
	}
	if len(m.history.retired) == maxReplayEvents {
		copy(m.history.retired, m.history.retired[1:])
		m.history.retired = m.history.retired[:maxReplayEvents-1]
	}
	m.history.retired = append(m.history.retired, ref)
}

func (m *Model) reconcileStreams() {
	kept := m.stream[:0]
	for _, entry := range m.stream {
		if m.persistedStream(entry) {
			m.retireStream(entry.messageRef)
		} else {
			kept = append(kept, entry)
		}
	}
	clear(m.stream[len(kept):])
	m.stream = kept
}

func journalText(s string, limit int) string {
	s = safeText(s)
	if len(s) > limit {
		return bounded(s, limit-40) + "\n[display truncated; artifact retained]"
	}
	return s
}
func (m *Model) journalLines() []string {
	lines := []string{m.historyHeader()}
	for i, row := range journalEntries(m.history.page.Entries) {
		msg := row.record.Message
		label := msg.Role
		switch msg.Role {
		case "user":
			label = "You"
		case "assistant":
			label = "Assistant"
		case "system":
			label = "Context evidence / system (not user intent)"
		case "tool":
			label = "Tool " + msg.ToolName + " [persisted result]"
			if msg.ToolName == "delegate" {
				label = "Child completion result [persisted summary/outcome]"
			}
			if row.parentID == "" {
				label += " [origin outside window or unavailable]"
			} else {
				label += " [assistant " + row.parentID[:12] + " / call " + msg.ToolCallID + "]"
			}
		}
		selected := row.record.ID == m.history.detailID
		collapsed := m.collapseTools && !selected
		text := label + ": " + msg.Content
		if msg.Role == "tool" {
			text = label + ": " + durableToolText(msg.ToolName, msg.ToolCallID, msg.Content)
			if collapsed {
				text = label + " [details collapsed; Ctrl+T]"
			}
		}
		for _, call := range msg.ToolCalls {
			text += "\nRequested tool " + call.Name + " / " + call.ID
			if !collapsed {
				text += "\nRequested arguments (not an original file diff): " + string(call.Args)
			}
		}
		if selected {
			return []string{m.historyHeader(), "Record " + row.record.ID + " · /journal to return · PgUp/PgDn", journalText(text, maxJournalText-8192)}
		}
		lines = append(lines, fmt.Sprintf("Record %d · /detail %d\n", m.history.page.Before+i+1, m.history.page.Before+i+1)+journalText(text, maxText))
	}
	if !m.history.older {
		// Query owns status, not conversation identity. A fallback is never inserted
		// into the durable journal or matched by content against a real message.
		if (!m.history.loaded || len(m.history.page.Entries) == 0) && m.state.Answer != "" {
			lines = append(lines, "Query answer fallback (not a journal record): "+m.state.Answer)
		}
		for _, entry := range m.stream {
			if m.persistedStream(entry) {
				continue
			}
			if entry.tool && m.collapseTools {
				lines = append(lines, entry.label+"[details collapsed; Ctrl+T]")
			} else {
				lines = append(lines, entry.label+entry.text)
			}
		}
	}
	if m.state.LastRunFailure != nil {
		lines = append(lines, "Failure: "+m.state.LastRunFailure.Reason+" "+m.state.LastRunFailure.Detail)
	}
	lines = append(lines, m.transcript...)
	return lines
}

// Preserve structured result metadata/log references and show captured output
// as scrollable text. No live ToolEnd is allowed to alter this durable record.
// Unknown/legacy payloads are shown as captured content, never inferred success.
func durableToolText(name, callID, content string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &fields) != nil || fields == nil {
		return content
	}
	// Successful read_file content is raw file data, not a result envelope.
	if name == "read_file" {
		return "[captured content; outcome metadata unavailable]\n" + content
	}
	status := toolOutcome(name, callID, content, fields)
	var output string
	for _, key := range []string{"output", "tail"} {
		var text string
		if strictField(fields[key], &text) {
			output += text
			delete(fields, key)
		}
	}
	metadata, _ := json.MarshalIndent(fields, "", "  ")
	return fmt.Sprintf("[%s]\n%s\n%s", status, output, metadata)
}

// JSON null is not a scalar zero value. In particular it must not certify a
// false failure flag or turn an invalid exit code into absent fallback evidence.
func strictField(raw json.RawMessage, into any) bool {
	return len(raw) != 0 && string(raw) != "null" && json.Unmarshal(raw, into) == nil
}

func toolOutcome(name, callID, content string, fields map[string]json.RawMessage) string {
	const unavailable = "outcome unavailable"
	var failed, interrupted, executionError bool
	var code *int
	if _, present := fields["toolCallId"]; present {
		// Ordinary tools.Result: identity selects this schema even if malformed.
		// Never reinterpret a contradictory identity as identity-less verification.
		var result tools.Result
		if json.Unmarshal([]byte(content), &result) != nil || result.ToolCallID == "" || result.ToolCallID != callID ||
			!strictField(fields["toolCallId"], &result.ToolCallID) || !strictField(fields["isError"], &result.IsError) ||
			!strictField(fields["output"], &result.Output) {
			return unavailable
		}
		for _, key := range []string{"exitCode", "executionError", "tail", "logRef", "truncated"} {
			if _, present := fields[key]; present {
				return unavailable // mixed schemas, not alternate exit evidence
			}
		}
		if raw, ok := fields["interrupted"]; ok && !strictField(raw, &result.Interrupted) {
			return unavailable
		}
		if raw, ok := fields["details"]; ok {
			var details map[string]json.RawMessage
			if !strictField(raw, &details) {
				return unavailable
			}
			for _, value := range details {
				var text string
				if !strictField(value, &text) {
					return unavailable // no partially decoded maps (including null values)
				}
			}
		}
		if value, ok := result.Details["executionError"]; ok {
			if value != "true" && value != "false" {
				return unavailable
			}
			executionError = value == "true"
		}
		if value, ok := result.Details["exitCode"]; ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return unavailable
			}
			code = &n
		}
		failed, interrupted = result.IsError, result.Interrupted
	} else {
		// workflows.verificationResult has no identity; all seven fields are
		// emitted (exitCode may be null). Keep this local read-only projection
		// separate from tools.Result; workflows must not depend on the TUI.
		var tail, logRef string
		var truncated bool
		if name != "run_command" || !strictField(fields["tail"], &tail) || !strictField(fields["logRef"], &logRef) ||
			!strictField(fields["isError"], &failed) || !strictField(fields["interrupted"], &interrupted) ||
			!strictField(fields["executionError"], &executionError) || !strictField(fields["truncated"], &truncated) ||
			json.Unmarshal(fields["exitCode"], &code) != nil {
			return unavailable
		}
		if strings.TrimSpace(logRef) == "" {
			return unavailable
		}
	}
	switch {
	case interrupted:
		return "interrupted"
	case failed || executionError:
		return "failed/denied (see captured result)"
	case name == "run_command" && code == nil:
		return "outcome unavailable (no captured exit code)"
	case name == "run_command" && *code != 0:
		return "failed"
	default:
		return "succeeded"
	}
}

func boundedJournal(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		remaining := maxJournalText - b.Len() - 80
		if len(line)+1 > remaining {
			if remaining > 40 {
				b.WriteString(journalText(line, remaining))
			}
			b.WriteString("\n[window display limit reached; use /detail <record>, /older or /latest]")
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
