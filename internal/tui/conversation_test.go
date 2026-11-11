package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/activities"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

func conversationFixture() []activities.TranscriptRecord {
	r := durableRecords(5)
	r[0].Message.Content = "Explain the project"
	r[1].Message = llm.Message{Role: "system", Content: strings.Repeat("PRIVATE-CONTEXT", 10000)}
	r[2].Message = llm.Message{Role: "assistant", Content: "INTERMEDIATE", ToolCalls: []llm.ToolCall{{ID: "reused", Name: "read_file", Args: []byte(`{"path":"PRIVATE-ARGS"}`)}}}
	r[3].Message = llm.Message{Role: "tool", ToolName: "read_file", ToolCallID: "reused", Content: "RAW-FILE [succeeded] [failed] Assistant"}
	r[4].Message = llm.Message{Role: "assistant", Content: "A readable answer"}
	return r
}

// Catch record dumps, ungrouped noise and response headings that claim verification.
func TestConversationCollapsedContent(t *testing.T) {
	m, _ := testModel(t)
	m.history.loaded = true
	m.history.page.Entries = conversationFixture()
	m.rebuildJournal()
	v := ansi.Strip(m.viewport.GetContent())
	for _, want := range []string{"› You", "Explain the project", "▸ Work", "3 observations", "Ctrl+T", "✓ Assistant · response completed", "A readable answer"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q: %s", want, v)
		}
	}
	for _, hidden := range []string{"PRIVATE-CONTEXT", "PRIVATE-ARGS", "RAW-FILE", "INTERMEDIATE", "Record ", "verified", "[succeeded]", "[failed]"} {
		if strings.Contains(v, hidden) {
			t.Errorf("normal conversation leaked %q", hidden)
		}
	}
	if strings.Count(v, "Work") != 1 {
		t.Fatal("expected one work block per prompt")
	}
}

func TestConversationGroupsPartialReusedAndZeroWork(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	second := conversationFixture()
	for i := range second {
		second[i].ID = fmt.Sprintf("%064x", 100+i)
	}
	second[0].Message.Content = "Second prompt"
	partial := durableRecords(1)[0]
	partial.ID = "earlier"
	partial.Message = r[3].Message
	m.history.page.Entries = append([]activities.TranscriptRecord{partial}, append(r, second...)...)
	m.history.page.Before = 20
	m.rebuildJournal()
	v := ansi.Strip(m.viewport.GetContent())
	if strings.Count(v, "▸ Work") != 3 || !strings.Contains(v, "earlier / partial") || strings.Count(v, "3 observations") != 2 {
		t.Fatalf("wrong prompt ownership: %s", v)
	}
	m.history.page.Entries = []activities.TranscriptRecord{r[0], r[4]}
	m.rebuildJournal()
	if strings.Contains(m.viewport.GetContent(), "Work") {
		t.Fatal("invented work for zero-work response")
	}
}

func TestConversationCollapsedFailureAuthority(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[3].Message = llm.Message{Role: "tool", ToolName: "run_command", ToolCallID: "reused", Content: `{"toolCallId":"reused","output":"PRIVATE-OUTPUT","isError":false,"details":{"exitCode":"2"}}`}
	m.history.page.Entries = r
	m.state.LastRunFailure = &workflows.Failure{Reason: "provider_error", Detail: "unrelated current request"}
	m.rebuildJournal()
	v := ansi.Strip(m.viewport.GetContent())
	for _, want := range []string{"[failed]", "Request failed", "provider_error", "✓ Assistant · response completed"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q: %s", want, v)
		}
	}
	if strings.Contains(v, "PRIVATE-OUTPUT") {
		t.Fatal("collapsed failure leaked output")
	}
	r[3].Message.Content = `{"toolCallId":"reused","output":"tests passed","isError":false}`
	m.rebuildJournal()
	if !strings.Contains(ansi.Strip(m.viewport.GetContent()), "[outcome unavailable]") {
		t.Fatal("unknown command outcome hidden")
	}
}

func TestConversationGroupToggleSelectionAndDraft(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = conversationFixture()
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.prompt.SetValue("draft\nkept")
	_, cmd := m.Update(key('t', tea.ModCtrl))
	if cmd != nil || m.prompt.Value() != "draft\nkept" {
		t.Fatal("toggle sent input or changed draft")
	}
	v := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(v, "▾ Work") || !strings.Contains(v, "RAW-FILE") || !strings.Contains(v, "PRIVATE-ARGS") {
		t.Fatalf("work not expanded: %s", v)
	}
	// Selection visits visible semantic targets, not hidden record indices.
	m.selectedID = m.history.page.Entries[3].ID
	m.Update(key('t', tea.ModCtrl))
	v = ansi.Strip(m.viewport.GetContent())
	if strings.Contains(v, "RAW-FILE") || !strings.Contains(v, "> ▸ Work") {
		t.Fatalf("collapse lost visible selected target: %s", v)
	}
	m.Update(key('t', tea.ModCtrl))
	m.selectedID = m.history.page.Entries[3].ID
	m.Update(key('o', tea.ModCtrl))
	if m.history.detailID != m.history.page.Entries[3].ID || !strings.Contains(m.viewport.GetContent(), "RAW-FILE") {
		t.Fatal("raw detail unreachable")
	}
}

func TestCompactConversationLayout(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {16, 5}, {8, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := testModel(t)
			m.history.page.Entries = conversationFixture()
			m.state.Session = &workflows.SessionInfo{Model: "original-model", Workdir: "/original/project"}
			m.state.Mode = "normal"
			m.state.Status = "idle"
			m.Update(operationMsg{epoch: m.epoch, label: "Submit", detail: "started"})
			m.prompt.SetValue("DRAFT\nKEPT")
			m.resize(size[0], size[1])
			top, _ := m.chrome()
			v := ansi.Strip(m.View().Content)
			if len(top) > 5 {
				t.Errorf("normal header consumes %d rows", len(top))
			}
			if lipgloss.Height(v) > size[1] || lipgloss.Width(v) > size[0] || (!strings.Contains(v, "DRAFT") && !strings.Contains(v, "KEPT")) || m.prompt.Value() != "DRAFT\nKEPT" {
				t.Fatalf("composer/frame clipped: %s", v)
			}
			if size[0] >= 80 {
				for _, want := range []string{"original-model", "/original/project", "session:one", "› You", "✓ Assistant", "▸ Work", "KEPT", "─", "Ctrl+T"} {
					if !strings.Contains(v, want) {
						t.Errorf("missing %q: %s", want, v)
					}
				}
			}
			for _, hidden := range []string{"Queued: 0", "Submit: started", "PRIVATE-CONTEXT", "Record 1"} {
				if strings.Contains(v, hidden) {
					t.Errorf("chrome/noise leak %q", hidden)
				}
			}
		})
	}
}

func TestConversationRoleStyleAndNoColorMeaning(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = conversationFixture()
	m.rebuildJournal()
	styled := m.viewport.GetContent()
	plain := ansi.Strip(styled)
	if !strings.Contains(plain, "› You") || !strings.Contains(plain, "✓ Assistant · response completed") {
		t.Fatal("roles not distinct without color")
	}
	// The body must not be painted as success just because its heading is green.
	if strings.Contains(styled, "\x1b") && !strings.Contains(styled, "\x1b[m\n  A readable answer") && !strings.Contains(styled, "\x1b[0m\n  A readable answer") {
		t.Fatalf("assistant body inherits semantic heading style: %q", styled)
	}
	t.Setenv("NO_COLOR", "1")
	m.rebuildJournal()
	if ansi.Strip(m.viewport.GetContent()) != plain {
		t.Fatal("NO_COLOR changed semantic text")
	}
}
