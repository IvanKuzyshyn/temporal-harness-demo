package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

func TestConversationLiveOwnershipAcrossQueryHistorySeam(t *testing.T) {
	for _, hasWork := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior-work=%v", hasWork), func(t *testing.T) {
			m, _ := testModel(t)
			m.resize(120, 40)
			a := conversationFixture()
			a[0].Message.Content = "PROMPT-A"
			a[1].Message.Content = "A-CONTEXT"
			a[4].Message.Content = "ANSWER-A"
			if !hasWork {
				a = []activities.TranscriptRecord{a[0], a[4]}
			}
			dir := m.opts.DataDir
			saveHistory(t, dir, m.opts.SessionID, a)
			historySnapshot(t, m, dir)
			// Save the actual rendered durable prompt/work/answer region, excluding
			// the history availability header. It must never acquire live ownership.
			durableRegion := func() string {
				t.Helper()
				v := ansi.Strip(m.viewport.GetContent())
				start, end := strings.Index(v, "› You"), strings.Index(v, "ANSWER-A")
				if start < 0 || end < start {
					t.Fatalf("missing durable A: %s", v)
				}
				return v[start : end+len("ANSWER-A")]
			}
			before := durableRegion()
			assertOwnership := func() {
				t.Helper()
				if got := durableRegion(); got != before {
					t.Fatalf("cached completed A acquired current work/activity:\n%s\nwant:\n%s", got, before)
				}
				frame := ansi.Strip(m.View().Content)
				answer, live := strings.Index(frame, "ANSWER-A"), strings.Index(frame, "live observations / unassociated")
				if answer < 0 || live <= answer || !strings.Contains(frame[live:], "Ctrl+T · generating") {
					t.Fatalf("actual frame must put explicitly unassociated activity AFTER completed A: %s", frame)
				}
			}
			working := workflows.State{Status: "working", Phase: "generating", Session: &workflows.SessionInfo{DataDir: dir}}
			m.queryID++
			_, delayed := m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: working})
			if delayed == nil || !m.history.pending {
				t.Fatal("fixture must leave B's asynchronous history read pending")
			}
			assertOwnership()
			m.Update(historyMsg{epoch: m.epoch, generation: m.history.generation, query: m.history.query, err: errors.New("history read failed")})
			assertOwnership()

			// Exercise actual event projection; reused call IDs do not associate
			// these lossy observations with either durable prompt.
			tool, _ := json.Marshal(events.ToolData{Name: "read_file", ToolCallID: "reused", Chunk: "LIVE-TOOL"})
			child, _ := json.Marshal(events.ChildData{ChildWorkflowID: "child", Task: "LIVE-CHILD"})
			m.applyEvent(compactEvent(events.Event{Seq: 1, Type: events.ToolUpdate, Data: tool}))
			m.applyEvent(compactEvent(events.Event{Seq: 2, Type: events.ChildStarted, Data: child}))
			m.refreshViewport(false)
			assertOwnership()
			m.selectedID = "work:live"
			m.Update(key('t', tea.ModCtrl))
			frame := ansi.Strip(m.View().Content)
			live := strings.Index(frame, "live observations / unassociated")
			if live < 0 || !strings.Contains(frame, "2 live observations / unassociated") || strings.Index(frame, "LIVE-TOOL") <= live || strings.Index(frame, "LIVE-CHILD") <= live {
				t.Fatalf("expanded observations not owned by separate live group: %s", frame)
			}
			m.applyEvent(compactEvent(events.Event{Seq: 3, Type: events.ToolUpdate, Data: tool}))
			m.refreshViewport(false)
			m.Update(key('t', tea.ModCtrl))
			m.Update(key('t', tea.ModCtrl))
			frame = ansi.Strip(m.View().Content)
			if strings.Count(frame, "LIVE-TOOL") != 1 || !strings.Contains(frame, "2 live observations / unassociated") {
				t.Fatalf("frozen expansion consumed incoming observations: %s", frame)
			}
			assertOwnership()

			// B's durable boundary now arrives. Only ordered durable work belongs
			// to B; merely having a newer prompt does not identify surviving events.
			b := durableRecords(8)[5:]
			b[0].Message.Content = "PROMPT-B"
			b[1].Message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "reused", Name: "read_file"}}}
			b[2].Message = llm.Message{Role: "tool", ToolName: "read_file", ToolCallID: "reused", Content: "B-DURABLE-TOOL"}
			saveHistory(t, dir, m.opts.SessionID, append(a, b...))
			m.Update(m.jumpLatest()())
			assertOwnership()
			m.selectedID = "work:" + b[0].ID
			m.Update(key('t', tea.ModCtrl))
			m.selectedID = "work:live"
			m.Update(key('t', tea.ModCtrl))
			frame = ansi.Strip(m.View().Content)
			promptB, durableB, live := strings.Index(frame, "PROMPT-B"), strings.Index(frame, "B-DURABLE-TOOL"), strings.Index(frame, "live observations / unassociated")
			if promptB < 0 || durableB <= promptB || live <= durableB || strings.Index(frame, "LIVE-CHILD") <= live || strings.Count(frame, "LIVE-TOOL") != 2 || !strings.Contains(frame, "3 live observations / unassociated") {
				t.Fatalf("later B page guessed live ownership or lost ordered durable work: %s", frame)
			}
			assertOwnership()
		})
	}
}
