package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"github.com/charmbracelet/x/ansi"
)

// These exercise the actual native frame, not an HTML or screenshot surrogate.
func TestVisualRoleSpacingAndContinuation(t *testing.T) {
	for _, width := range []int{80, 120} {
		for _, noColor := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/no-color=%v", width, noColor), func(t *testing.T) {
				if noColor {
					t.Setenv("NO_COLOR", "1")
				}
				m, _ := testModel(t)
				r := conversationFixture()
				r[4].Message.Content = strings.Repeat("readable words ", 10) + "LAST-WORD"
				next := durableRecords(1)[0]
				next.ID = "next-prompt"
				next.Message.Content = "Next prompt"
				m.history.page.Entries = append(r, next)
				m.prompt.SetValue("DRAFT\nKEPT")
				m.resize(width, 40)
				frame := ansi.Strip(m.View().Content)
				lines := strings.Split(frame, "\n")
				answer := -1
				for i, line := range lines {
					if strings.Contains(line, "✓ Assistant") {
						answer = i
						if i == 0 || strings.TrimSpace(lines[i-1]) != "" {
							t.Errorf("no separator before assistant: %s", frame)
						}
					}
				}
				if answer < 0 {
					t.Fatal("answer missing")
				}
				for i := answer + 1; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
					if !strings.HasPrefix(lines[i], "  ") {
						t.Errorf("body continuation lost inset: %q", lines[i])
					}
				}
				if !strings.Contains(frame, "LAST-WORD") || !strings.Contains(frame, "KEPT") {
					t.Fatal("answer/composer lost")
				}
				for i, line := range lines {
					if strings.Contains(line, "› You") && i > answer && strings.TrimSpace(lines[i-1]) != "" {
						t.Errorf("next turn lacks separation: %s", frame)
					}
				}
				// The word fits on a fresh line; it must not be split by cell wrapping.
				r[4].Message.Content = strings.Repeat("x", width-10) + " finalword"
				m.history.page.Entries = r
				m.rebuildJournal()
				m.viewport.GotoTop()
				if !strings.Contains(ansi.Strip(m.View().Content), "  finalword") {
					t.Fatal("word was split instead of wrapped")
				}
			})
		}
	}
}

func TestVisualHistoryFailureIsConciseAndInspectable(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(fmt.Sprint(loaded), func(t *testing.T) {
			m, _ := testModel(t)
			if loaded {
				m.history.loaded = true
				m.history.page.Entries = conversationFixture()
			}
			raw := "openat " + strings.Repeat("abcdef0123456789", 4) + ".transcript.json: no such file or directory"
			m.history.err = raw
			m.state.Status = "working"
			m.state.Phase = "context"
			m.prompt.SetValue("UNSENT\nKEPT")
			m.resize(80, 24)
			styled := m.View().Content
			frame := ansi.Strip(styled)
			if strings.Count(frame, "History unavailable") != 1 || strings.Contains(frame, "abcdef") || !strings.Contains(frame, "/history-error") || !strings.Contains(frame, "/latest") {
				t.Errorf("noisy/inaccessible history error: %s", frame)
			}
			if !strings.Contains(styled, "38;5;203") {
				t.Error("history failure lacks red notice")
			}
			for _, line := range strings.Split(frame, "\n") {
				if strings.Contains(line, "unassociated") && (!strings.Contains(line, "context") || !strings.Contains(line, "Ctrl+T")) {
					t.Errorf("live work must fit one row: %q", line)
				}
			}
			if loaded && (!strings.Contains(frame, "Explain the project") || !strings.Contains(frame, "A readable answer")) {
				t.Fatal("cached conversation lost during work")
			}
			if !loaded && strings.Contains(frame, "› You") {
				t.Fatal("invented durable prompt")
			}
			m.prompt.SetValue("/history-error")
			_, cmd := m.Update(key(tea.KeyEnter, 0))
			if cmd != nil || !strings.Contains(ansi.Strip(m.viewport.GetContent()), raw) {
				t.Fatal("explicit diagnostic unavailable or sent to backend")
			}
			if strings.Contains(ansi.Strip(m.View().Content), "Record detail") {
				t.Error("synthetic diagnostic mislabeled as durable record detail")
			}
			m.prompt.SetValue("DRAFT\nKEPT")
			m.Update(key('o', tea.ModCtrl))
			if strings.Contains(m.viewport.GetContent(), raw) || m.prompt.Value() != "DRAFT\nKEPT" || m.selectedID != "" || strings.Contains(m.notice, "No loaded records") {
				t.Fatal("diagnostic return/draft broken or selected an unrelated durable record")
			}
		})
	}
}

func TestVisualLoadedConversationDuringWork(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := testModel(t)
			m.history.loaded = true
			m.history.page.Entries = conversationFixture()[:4]
			m.state.Status, m.state.Phase = "working", "generating"
			m.prompt.SetValue("UNSENT_FIRST\nUNSENT_SECOND")
			m.resize(size[0], size[1])
			frame := ansi.Strip(m.View().Content)
			for _, want := range []string{"› You", "Explain the project", "3 observations", "live observations / unassociated · Ctrl+T · generating", "UNSENT_FIRST", "UNSENT_SECOND"} {
				if !strings.Contains(frame, want) {
					t.Errorf("active frame lost %q: %s", want, frame)
				}
			}
			for _, hidden := range []string{"History unavailable", "PRIVATE-CONTEXT", "PRIVATE-ARGS", "RAW-FILE", "response completed"} {
				if strings.Contains(frame, hidden) {
					t.Errorf("active frame invented/leaked %q", hidden)
				}
			}
		})
	}
}

func TestVisualExpandedReceiptsRemainScannable(t *testing.T) {
	m, _ := testModel(t)
	r := conversationFixture()
	r[1].Message.Content = strings.Repeat("CONTEXT noise and evidence\n", 200) + "FULL-CONTEXT-END"
	m.history.page.Entries = r
	m.resize(120, 40)
	m.viewport.GotoTop()
	m.prompt.SetValue("DRAFT\nKEPT")
	m.Update(key('t', tea.ModCtrl))
	m.viewport.GotoTop()
	frame := ansi.Strip(m.View().Content)
	for _, want := range []string{"Context evidence", "Assistant · tool request", "Tool read_file", "A readable answer", "preview", "/detail 2", "KEPT"} {
		if !strings.Contains(frame, want) {
			t.Errorf("expanded receipt obscured %q: %s", want, frame)
		}
	}
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "CONTEXT noise") && !strings.HasPrefix(line, "    ") {
			t.Errorf("receipt payload lost gutter: %q", line)
		}
	}
	if strings.Contains(frame, "FULL-CONTEXT-END") {
		t.Fatal("unbounded context leaked into preview")
	}
	m.prompt.SetValue("/detail 2")
	m.submitPrompt(false)
	if m.history.detailID != r[1].ID || !strings.Contains(m.viewport.GetContent(), "FULL-CONTEXT-END") {
		t.Fatal("bounded preview lost full source")
	}
}

func TestVisualHeaderPreservesDistinguishingIdentity(t *testing.T) {
	for _, width := range []int{80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _ := testModel(t)
			m.opts.SessionID = "session-conversation-real-a1b2c3d4e5"
			root := "/private/var/folders/" + strings.Repeat("temporary-prefix/", 6) + "ordinary-source"
			m.state.Session = &workflows.SessionInfo{Model: "original-model", Workdir: root}
			m.resize(width, 40)
			top, _ := m.chrome()
			if len(top) > 5 || !strings.Contains(top[0], "a1b2c3d4e5") || strings.Contains(top[0], "session:session-") || !strings.Contains(top[1], "ordinary-source") || !strings.Contains(top[1], "/private/") {
				t.Errorf("identity spent on shared prefixes: %q", top)
			}
			m.prompt.SetValue("/metadata")
			m.submitPrompt(false)
			if !strings.Contains(m.viewport.GetContent(), root) || !strings.Contains(m.viewport.GetContent(), m.opts.SessionID) {
				t.Fatal("authoritative full identity lost")
			}
		})
	}
}
