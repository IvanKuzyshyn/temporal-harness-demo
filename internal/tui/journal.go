package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
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
	if entry.messageRef == "" || entry.parentWorkflowID != "" || (entry.workflowID != "" && entry.workflowID != m.opts.SessionID) {
		return false
	}
	ref := activities.TranscriptRef(m.opts.SessionID)
	for _, r := range m.history.page.Entries {
		if entry.messageRef == messageRefIdentity(ref+"#"+r.ID) {
			return true
		}
	}
	return false
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
	// Never certify an outcome from JSON-shaped repository text.
	var identity string
	_ = json.Unmarshal(fields["toolCallId"], &identity)
	envelope := identity != "" && identity == callID && name != "read_file"
	if name == "run_command" && fields["tail"] != nil && fields["logRef"] != nil && fields["exitCode"] != nil {
		envelope = true // durable after-run verification result schema
	}
	if !envelope {
		return "[captured content; outcome metadata unavailable]\n" + content
	}
	status := "outcome unavailable"
	var failed, interrupted bool
	if raw, ok := fields["isError"]; ok && string(raw) != "null" && json.Unmarshal(raw, &failed) == nil {
		if failed {
			status = "failed/denied (see captured result)"
		} else {
			status = "succeeded"
		}
	}
	if name == "run_command" && status == "succeeded" {
		// isError=false alone is not execution evidence. Require an actual
		// captured exit code, including the after-run verification schema.
		var code *int
		if err := json.Unmarshal(fields["exitCode"], &code); err != nil {
			code = nil
		}
		var details map[string]string
		_ = json.Unmarshal(fields["details"], &details)
		if code == nil {
			if n, err := strconv.Atoi(details["exitCode"]); err == nil {
				code = &n
			}
		}
		switch {
		case details["executionError"] == "true":
			status = "failed"
		case code == nil:
			status = "outcome unavailable (no captured exit code)"
		case *code != 0:
			status = "failed"
		}
	}
	if json.Unmarshal(fields["interrupted"], &interrupted) == nil && interrupted {
		status = "interrupted"
	}
	var output string
	for _, key := range []string{"output", "tail"} {
		var text string
		if json.Unmarshal(fields[key], &text) == nil {
			output += text
			delete(fields, key)
		}
	}
	metadata, _ := json.MarshalIndent(fields, "", "  ")
	return fmt.Sprintf("[%s]\n%s\n%s", status, output, metadata)
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
