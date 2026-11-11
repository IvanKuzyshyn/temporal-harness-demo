package tui

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

func TestQueueSnapshotsAndFooter(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 14}} {
		m, _ := testModel(t)
		m.resize(size[0], size[1])
		for _, inbox := range [][]workflows.MessageInput{
			{{Text: "alpha", WhenBusy: "steer"}},
			{{Text: "beta", WhenBusy: "followUp"}},
			nil,
		} {
			m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Status: "idle", Inbox: inbox}})
			view := ansi.Strip(m.View().Content)
			for _, want := range []string{"Queued:", "session:one", "turn:0/8 idle", "UI:"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%v missing %q: %s", size, want, view)
				}
			}
			if len(inbox) > 0 {
				if !strings.Contains(view, inbox[0].WhenBusy+": "+inbox[0].Text) {
					t.Fatalf("queue preview missing: %s", view)
				}
			} else if !strings.Contains(view, "Queued: 0") || strings.Contains(view, "beta") {
				t.Fatalf("queue not cleared: %s", view)
			}
			if strings.Contains(view, "alpha") && len(inbox) == 1 && inbox[0].Text == "beta" {
				t.Fatal("stale preview")
			}
			if lipgloss.Width(view) > size[0] || lipgloss.Height(view) > size[1] {
				t.Fatal("view exceeds terminal")
			}
		}
	}
}

func TestQueueProjectionAndOverflow(t *testing.T) {
	inbox := make([]workflows.MessageInput, 100)
	for i := range inbox {
		inbox[i] = workflows.MessageInput{Text: strings.Repeat("界🙂\n\t\x1b\x00\u202e", 1000), WhenBusy: strings.Repeat("followUp\x1b\n", 100), RequestID: "private-id"}
	}
	projected := displayState(workflows.State{Inbox: inbox, TranscriptRef: "private", LastRunFailure: &workflows.Failure{Reason: "failed"}})
	if len(projected.Inbox) != 64 {
		t.Fatalf("retained %d entries", len(projected.Inbox))
	}
	for _, msg := range projected.Inbox {
		if len(msg.Text) > 256 || len(msg.WhenBusy) > 32 || msg.RequestID != "" {
			t.Fatalf("unbounded/private projection: %+v", msg)
		}
		for _, s := range []string{msg.Text, msg.WhenBusy} {
			if !utf8.ValidString(s) {
				t.Fatal("invalid UTF-8")
			}
			for _, r := range s {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					t.Fatalf("unsafe rune %q", r)
				}
			}
		}
	}
	inbox[0].Text = "mutated"
	if projected.Inbox[0].Text == "mutated" {
		t.Fatal("projection aliases snapshot")
	}
	m, _ := testModel(t)
	m.state = projected
	for _, size := range [][2]int{{80, 24}, {40, 14}, {1, 1}, {12, 6}, {0, 0}, {1000, 500}} {
		m.resize(size[0], size[1])
		view := ansi.Strip(m.View().Content)
		if size[0] >= 40 {
			for _, want := range []string{"Queued: 64+", "followUp", "界"} {
				if !strings.Contains(view, want) {
					t.Fatalf("missing %q: %s", want, view)
				}
			}
			if strings.Contains(strings.ToLower(view), "paused") {
				t.Fatal("failure must not imply queue paused")
			}
		}
		if lipgloss.Width(view) > size[0] || (view != "" && lipgloss.Height(view) > size[1]) {
			t.Fatalf("size overflow %v", size)
		}
	}
	m.state = displayState(workflows.State{Inbox: make([]workflows.MessageInput, 64)})
	m.resize(80, 24)
	if strings.Contains(m.View().Content, "64+") {
		t.Fatal("exactly 64 is not overflow")
	}
}

func TestQueueTwoModesRendered(t *testing.T) {
	m, _ := testModel(t)
	m.resize(80, 24)
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{Inbox: []workflows.MessageInput{{Text: "first", WhenBusy: "steer"}, {Text: "next", WhenBusy: "followUp"}, {Text: "later", WhenBusy: "steer"}}}})
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Queued: 3", "steer: first", "followUp: next", "+1 more"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
}
