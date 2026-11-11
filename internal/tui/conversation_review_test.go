package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

func TestCompactSubmitReceiptCannotHideRequestError(t *testing.T) {
	m, _ := testModel(t)
	m.Update(operationMsg{epoch: m.epoch, label: "Submit", err: errors.New("started")})
	v := m.View().Content
	if !strings.Contains(ansi.Strip(v), "Request failed · Submit: started") || !strings.Contains(v, "38;5;203") {
		t.Fatal("request error hidden or not distinguished from success receipt")
	}
	m.Update(operationMsg{epoch: m.epoch, label: "Submit", detail: "started"})
	if strings.Contains(m.View().Content, "Submit: started") || strings.Contains(m.View().Content, "Request failed") {
		t.Fatal("successful submit left stale receipt/failure chatter")
	}
}

func TestCompactWorkToggleUsesActualVisibleHeightAfterNotice(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[0].Message.Content = strings.Repeat("line\n", 10) + "end"
	m.history.page.Entries = r
	m.rebuildJournal()
	m.browse()
	m.viewport.GotoTop()
	m.notice = "An exceptional notice"
	if strings.Contains(ansi.Strip(m.View().Content), "▸ Work") {
		t.Fatal("fixture should place work just outside visible frame")
	}
	m.Update(key('t', tea.ModCtrl))
	if len(m.expandedWork) != 0 {
		t.Fatal("Ctrl+T targeted an offscreen group using stale viewport geometry")
	}
}

func TestConversationDetailHeaderAnchorIsNotItsSourceBody(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = conversationFixture()
	m.prompt.SetValue("/detail 2")
	m.submitPrompt(false)
	before := m.viewport.View()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.viewport.View() != before || m.viewport.YOffset() != 0 {
		t.Fatal("detail header anchor was incorrectly attributed to source body")
	}
}

func TestConversationDetailReturnHasVisibleSelection(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = conversationFixture()
	m.rebuildJournal()
	m.prompt.SetValue("/detail 4")
	m.submitPrompt(false)
	m.Update(key('o', tea.ModCtrl))
	if !strings.Contains(ansi.Strip(m.viewport.GetContent()), "> ▸ Work") {
		t.Fatal("detail return leaves a hidden selected ID without a visible target")
	}
}

func TestConversationDetailHeadingSanitizesUntrustedMetadata(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[3].Message.ToolName = "evil\x1b[31m\x07\u202e"
	m.history.page.Entries = r
	m.history.detailID = r[3].ID
	plain := strings.Join(m.journalLines(), "\n")
	if strings.ContainsAny(plain, "\x1b\x07\u202e") {
		t.Fatal("untrusted detail heading retained terminal controls")
	}
}

func TestConversationFrozenWorkToggleDoesNotConsumeIncoming(t *testing.T) {
	m, _ := testModel(t)
	m.history.page.Entries = conversationFixture()
	m.rebuildJournal()
	m.viewport.GotoTop()
	m.browse()
	m.stream = append(m.stream, streamEntry{tool: true, label: "Live tool event", text: "NEW-WORK-NOT-YET-SEEN"})
	m.refreshViewport(false)
	m.Update(key('t', tea.ModCtrl))
	if strings.Contains(m.viewport.GetContent(), "NEW-WORK-NOT-YET-SEEN") {
		t.Fatal("expansion consumed live updates behind frozen window")
	}
	m.jumpLatest()
	v := ansi.Strip(m.viewport.GetContent())
	if !strings.Contains(v, "3 observations · Ctrl+T") || !strings.Contains(v, "1 live observations / unassociated") {
		t.Fatal("End did not expose newest observations separately from durable prompt work")
	}
}

func TestConversationWorkingWithoutDurableToolStillHasOneWorkBlock(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	m.history.page.Entries = r[:1]
	m.state = workflows.State{Status: "working", Phase: "generating", Mode: "normal", ActiveTools: []llm.ToolCall{{Name: "read_file"}}}
	m.rebuildJournal()
	v := ansi.Strip(m.viewport.GetContent())
	if strings.Count(v, "▸ Work") != 1 || !strings.Contains(v, "live observations / unassociated · Ctrl+T · generating") {
		t.Fatalf("active work absent: %s", v)
	}
	m.state.Status = "idle"
	m.rebuildJournal()
	if strings.Contains(m.viewport.GetContent(), "Work") {
		t.Fatal("settled zero-work prompt has invented observations")
	}
}

func TestCompactRoleBodiesAreIndentedAndPhaseIsNotDuplicated(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[0].Message.Content = "first\nsecond"
	r[4].Message.Content = "answer\ncontinued"
	m.history.page.Entries = r
	m.state = workflows.State{Status: "working", Phase: "generating", Mode: "normal"}
	m.rebuildJournal()
	v := ansi.Strip(m.viewport.GetContent())
	for _, want := range []string{"\n  first\n  second", "\n  answer\n  continued"} {
		if !strings.Contains(v, want) {
			t.Errorf("role body lost left margin: %q", want)
		}
	}
	top, _ := m.chrome()
	if strings.Contains(strings.Join(top, "\n"), "mode:normal") {
		t.Fatal("compact phase repeats mode metadata")
	}
}
