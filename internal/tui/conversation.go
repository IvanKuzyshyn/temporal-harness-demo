package tui

import (
	"fmt"
	"strings"
)

// conversationBlock is the single identity/semantic mapping used by rendering,
// selection and anchors. id is a durable record ID, or a namespaced work ID
// derived from the owning prompt (never a call ID or a rendered row index).
// sourceID is empty for synthetic/live blocks. Only trusted headings are styled.
// Bodies stay neutral, including raw evidence that happens to contain headings.
// Projection text is transient: rebuildJournal retains it only in journalParts,
// with headingBytes describing the trusted style span in that SAME string.
type conversationBlock struct {
	id, sourceID, groupID string
	heading, body, tone   string
	members               []string
	headingBytes          int // retained prefix length; heading/body are cleared after rendering
}

func (b conversationBlock) text(selected string) string {
	heading := safeText(b.heading)
	if b.id != "" && b.id == selected {
		heading = "> " + heading
	}
	if b.body != "" {
		return heading + "\n" + b.body
	}
	return heading
}

func roleBody(text string) string {
	return "  " + strings.ReplaceAll(journalText(text, maxText), "\n", "\n  ")
}

func recordBody(row journalEntry, limit int) string {
	msg := row.record.Message
	text := msg.Content
	if msg.Role == "tool" {
		text = durableToolText(msg.ToolName, msg.ToolCallID, msg.Content)
	}
	for _, call := range msg.ToolCalls {
		text += "\nRequested tool " + call.Name + " / " + call.ID + "\nRequested arguments (not an original file diff): " + string(call.Args)
	}
	return journalText(text, limit)
}

// Only the canonical parser's FIRST generated line supplies semantics. Never
// search captured bytes for status words; read_file is always opaque evidence.
func recordOutcome(row journalEntry) string {
	msg := row.record.Message
	if msg.Role != "tool" || msg.ToolName == "read_file" {
		return ""
	}
	line, _, _ := strings.Cut(durableToolText(msg.ToolName, msg.ToolCallID, msg.Content), "\n")
	return strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
}
func outcomeTone(status string) string {
	switch {
	case status == "succeeded":
		return "complete"
	case strings.HasPrefix(status, "failed"):
		return "failure"
	case status != "":
		return "attention"
	}
	return "muted"
}

func (m *Model) conversation() []conversationBlock {
	rows := journalEntries(m.history.page.Entries)
	blocks := []conversationBlock{{heading: m.historyHeader(), tone: "muted"}}
	if m.history.detailID != "" {
		for _, row := range rows {
			if row.record.ID == m.history.detailID {
				label := row.record.Message.Role
				if label == "tool" {
					label = "Tool " + row.record.Message.ToolName
					if row.parentID == "" {
						label += " [origin outside window or unavailable]"
					}
				}
				return append(blocks, conversationBlock{id: row.record.ID, sourceID: row.record.ID, heading: "Record " + row.record.ID + " · " + label + "\nCtrl+O /journal return · Ctrl+L /log captured log · PgUp/PgDn", body: recordBody(row, maxJournalText-8192)})
			}
		}
	}
	// Group the loaded window by actual order. Preceding work has its own partial
	// identity and cannot be attributed to a later, unrelated prompt.
	type group struct {
		id       string
		partial  bool
		work     []journalEntry
		answers  []journalEntry
		live     []streamEntry
		activity string
	}
	g := group{partial: true}
	valid := map[string]bool{}
	flush := func() {
		if len(g.work)+len(g.live) > 0 || g.activity != "" {
			if g.id == "" {
				g.id = "work:live"
			}
			valid[g.id] = true
			mark := "▸"
			if m.expandedWork[g.id] {
				mark = "▾"
			}
			heading := fmt.Sprintf("%s Work · %d observations", mark, len(g.work)+len(g.live))
			if g.id == "work:live" {
				heading += " · current live / unassociated"
			} else if g.partial {
				heading += " · earlier / partial"
			}
			heading += " · Ctrl+T"
			if g.activity != "" {
				heading += " · current activity: " + g.activity
			}
			counts := map[string]int{}
			members := []string{}
			for _, row := range g.work {
				members = append(members, row.record.ID)
				if status := recordOutcome(row); status != "" && status != "succeeded" {
					counts[status]++
				}
			}
			tone := "muted"
			if g.activity != "" {
				tone = "attention"
			}
			// Fixed order and a bounded set of parser statuses, never payload prose.
			for _, status := range []string{"failed", "failed/denied (see captured result)", "interrupted", "outcome unavailable", "outcome unavailable (identity mismatch)"} {
				if counts[status] > 0 {
					heading += fmt.Sprintf(" · %d [%s]", counts[status], status)
					if tone != "failure" {
						tone = outcomeTone(status)
					}
					delete(counts, status)
				}
			}
			// Parser unknown variants are normalized, not repeated as long notices.
			if len(counts) > 0 {
				heading += " · [outcome unavailable]"
				if tone != "failure" {
					tone = "attention"
				}
			}
			blocks = append(blocks, conversationBlock{id: g.id, groupID: g.id, heading: heading, tone: tone, members: members})
			if m.expandedWork[g.id] {
				for _, row := range g.work {
					label := "Context evidence / system (not user intent)"
					switch row.record.Message.Role {
					case "assistant":
						label = "Assistant · tool request"
					case "tool":
						label = "Tool " + singleLine(row.record.Message.ToolName) + " · captured result"
					}
					status := recordOutcome(row)
					if status != "" {
						label += " [" + status + "]"
					}
					n := 0
					for i, r := range rows {
						if r.record.ID == row.record.ID {
							n = m.history.page.Before + i + 1
							break
						}
					}
					label = fmt.Sprintf("  ↳ %s · /detail %d", label, n)
					blocks = append(blocks, conversationBlock{id: row.record.ID, sourceID: row.record.ID, groupID: g.id, heading: label, body: recordBody(row, maxText), tone: outcomeTone(status)})
				}
				for _, entry := range g.live {
					blocks = append(blocks, conversationBlock{groupID: g.id, heading: "  ↳ " + singleLine(entry.label), body: journalText(entry.text, maxText), tone: "muted"})
				}
			}
		}
		for _, row := range g.answers {
			blocks = append(blocks, conversationBlock{id: row.record.ID, sourceID: row.record.ID, heading: "✓ Assistant · response completed", body: roleBody(row.record.Message.Content) + "\n", tone: "complete"})
		}
	}
	for _, row := range rows {
		msg := row.record.Message
		if msg.Role == "user" {
			flush()
			g = group{id: "work:" + row.record.ID}
			blocks = append(blocks, conversationBlock{id: row.record.ID, sourceID: row.record.ID, heading: "› You", body: roleBody(msg.Content), tone: "user"})
		} else if msg.Role == "assistant" && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) != "" {
			g.answers = append(g.answers, row)
		} else {
			if g.id == "" {
				g.id = "work:partial:" + row.record.ID
			}
			g.work = append(g.work, row)
		}
	}
	flush()
	// Query activity and surviving stream observations have no trustworthy
	// prompt identity. Even the latest loaded prompt may be from a completed
	// request while a new history read is delayed or unavailable. Keep this
	// suffix separate; neither call IDs nor prose establish prompt ownership.
	g = group{id: "work:live"}
	var drafts []streamEntry
	if !m.history.older {
		stream := m.stream
		if !m.followLive {
			stream = m.browseStream
		}
		for _, entry := range stream {
			if m.persistedStream(entry) {
				continue
			}
			if entry.tool || entry.parentKey != "" || (entry.workflowKey != "" && entry.workflowKey != optionalIdentity(m.opts.SessionID)) || strings.HasPrefix(entry.label, "Live child") {
				g.live = append(g.live, entry)
			} else {
				drafts = append(drafts, entry)
			}
		}
		if m.state.Status == "working" || m.state.Status == "blocked" {
			g.activity = singleLine(m.state.Phase)
			if g.activity == "" {
				g.activity = "unknown"
			}
		}
	}
	flush()
	for id := range m.expandedWork {
		if !valid[id] {
			delete(m.expandedWork, id)
		}
	}
	if !m.history.older {
		if (!m.history.loaded || len(rows) == 0) && m.state.Answer != "" {
			blocks = append(blocks, conversationBlock{heading: "Assistant · query fallback (not a journal record)", body: journalText(m.state.Answer, maxAnswer), tone: "attention"})
		}
		for _, entry := range drafts {
			blocks = append(blocks, conversationBlock{heading: "Assistant · stream draft (best-effort)", body: journalText(entry.text, maxAnswer), tone: "attention"})
		}
	}
	if m.state.LastRunFailure != nil {
		blocks = append(blocks, conversationBlock{heading: "Request failed · current query (not attributed to a historic response)", body: journalText(m.state.LastRunFailure.Reason+" "+m.state.LastRunFailure.Detail, maxText), tone: "failure"})
	}
	for _, text := range m.transcript {
		blocks = append(blocks, conversationBlock{body: text})
	}
	return blocks
}

func (m *Model) toggleWork() {
	if m.history.detailID != "" || m.log.ref != "" || m.showPatterns {
		m.notice = "Return to conversation with Ctrl+O /journal before toggling work"
		return
	}
	target := ""
	// A selected member resolves through the same mapping even if collapsed.
	for _, b := range m.journalBlocks {
		if b.id == m.selectedID {
			target = b.groupID
		}
		for _, id := range b.members {
			if id == m.selectedID {
				target = b.groupID
			}
		}
	}
	if target == "" {
		row := 0
		for i, b := range m.journalBlocks {
			end := row + partRows(m.journalParts[i], m.width)
			if b.id == b.groupID && b.id != "" && end > m.viewport.YOffset() && row < m.viewport.YOffset()+m.viewport.Height() {
				target = b.id
			}
			row = end
		}
	}
	if target == "" {
		m.notice = "No visible work block · PgUp/PgDn or Ctrl+↑/↓ to find one"
		return
	}
	m.captureAnchor()
	saved := m.anchor
	m.browse()
	if m.expandedWork == nil {
		m.expandedWork = map[string]bool{}
	}
	if m.expandedWork[target] {
		delete(m.expandedWork, target)
	} else {
		m.expandedWork[target] = true
	}
	m.selectedID = target
	m.rebuildJournal()
	m.anchor = saved
	m.restoreAnchor()
	m.revealSelected()
}

func (m *Model) revealSelected() {
	row := 0
	for i, b := range m.journalBlocks {
		if b.id == m.selectedID {
			if row < m.viewport.YOffset() || row >= m.viewport.YOffset()+m.viewport.Height() {
				m.viewport.SetYOffset(row)
			}
			return
		}
		row += partRows(m.journalParts[i], m.width)
	}
}
