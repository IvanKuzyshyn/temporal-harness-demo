package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestJournalPinnedGeometry(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {38, 14}, {16, 5}, {8, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := testModel(t)
			m.prompt.SetValue("DRAFT")
			m.state.Session = &workflows.SessionInfo{Model: strings.Repeat("model", 60), Workdir: strings.Repeat("/root", 100)}
			m.notice = strings.Repeat("notice", 100)
			m.transcript = []string{strings.Repeat("journal\n", 200)}
			m.resize(size[0], size[1])
			v := m.View().Content
			if lipgloss.Height(v) > size[1] || lipgloss.Width(v) > size[0] {
				t.Fatalf("unbounded %dx%d: %s", lipgloss.Width(v), lipgloss.Height(v), v)
			}
			if !strings.Contains(v, "DRAFT") {
				t.Fatalf("prompt clipped: %s", v)
			}
		})
	}
}
func TestJournalBrowsingFreezesIncomingAndEndReturns(t *testing.T) {
	m, _ := testModel(t)
	m.transcript = []string{strings.Repeat("older line\n", 100)}
	m.refreshViewport(true)
	m.prompt.SetValue("keep draft")
	m.Update(key(tea.KeyPgUp, 0))
	before := m.viewport.View()
	m.applyEvent(delta(1, "incoming"))
	m.refreshViewport(false)
	if m.viewport.View() != before {
		t.Fatal("reading anchor moved")
	}
	if !strings.Contains(m.View().Content, "1 unseen") {
		t.Fatal("unseen update not indicated")
	}
	m.Update(key(tea.KeyEnd, 0))
	if !strings.Contains(m.View().Content, "LIVE") || strings.Contains(m.View().Content, "1 unseen") {
		t.Fatal("End did not follow latest")
	}
	if m.prompt.Value() != "keep draft" {
		t.Fatal("navigation destroyed draft")
	}
}
func TestJournalMouseWheel(t *testing.T) {
	m, _ := testModel(t)
	m.transcript = []string{strings.Repeat("row\n", 100)}
	m.refreshViewport(true)
	old := m.viewport.YOffset()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.viewport.YOffset() >= old {
		t.Fatal("mouse did not scroll")
	}
}
func TestTinyApprovalRefusesHiddenDecision(t *testing.T) {
	m, _ := testModel(t)
	p := &workflows.PendingApproval{RequestID: "r", ActionHash: "h", ExpiresAt: time.Now().Add(time.Hour)}
	m.syncApproval(p)
	m.state.PendingApproval = p
	m.resize(20, 5)
	if !strings.Contains(strings.ToLower(m.View().Content), "resize") {
		t.Fatal("missing resize warning")
	}
	_, cmd := m.Update(key('a', 0))
	if cmd != nil || m.approval.pending {
		t.Fatal("hidden approval dispatched")
	}
	m.approval.denying = true
	m.approval.reason = "preserve reason"
	_, cmd = m.Update(key(tea.KeyEnter, 0))
	if cmd != nil {
		t.Fatal("hidden denial dispatched")
	}
	m.resize(80, 24)
	if m.approval.reason != "preserve reason" {
		t.Fatal("reason lost")
	}
}
func TestSanitizedSessionDoesNotInventMismatch(t *testing.T) {
	m, _ := testModel(t)
	raw := &workflows.SessionInfo{Model: "mock", Workdir: "/tmp/a\x1bb", DataDir: m.opts.DataDir}
	m.opts.Input.Workdir = raw.Workdir
	m.configMismatch = m.sessionMismatch(raw)
	m.state = displayState(workflows.State{Session: raw})
	if strings.Contains(m.sessionConfiguration(), "mismatch") {
		t.Fatal("display sanitization changed semantic comparison")
	}
}
