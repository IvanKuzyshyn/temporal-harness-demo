package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestGapSuffixSurvivesRecovery(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "pending"}[pending], func(t *testing.T) {
			m, _ := testModel(t)
			m.started = true
			m.reconnect = nil
			m.lastSeq = 4
			m.lastSnapshot = time.Now()
			if pending {
				m.beginQuery()
			}
			m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(6, "six"), delta(7, "seven")}, offset: 700, at: time.Now()})
			if pending {
				old := m.queryID
				m.Update(snapshotMsg{epoch: m.epoch, query: old, state: workflows.State{LastSeq: 4}, at: time.Now()})
				if m.queryID == old {
					t.Fatal("gap did not retry")
				}
				m.Update(snapshotMsg{epoch: m.epoch, query: old, state: workflows.State{LastSeq: 99}})
			}
			m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{LastSeq: 5}, at: time.Now()})
			text := m.viewport.GetContent()
			if strings.Count(text, "six") != 1 || strings.Count(text, "seven") != 1 {
				t.Fatalf("lost/duplicated suffix: %s", text)
			}
			m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(8, "eight")}, at: time.Now()})
			if m.lastSeq != 8 || m.queryPending {
				t.Fatal("next event caused another gap")
			}
		})
	}
}

func TestSessionReturnRereadsUncoveredDeltas(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolved", true: "pending"}[pending], func(t *testing.T) {
			m, f := testModel(t)
			store := events.NewStore(m.opts.DataDir)
			for i := 1; i <= 15; i++ {
				text := "old"
				if i > 10 {
					text = "live"
				}
				if _, err := store.Append(events.Scope{StreamID: "one", WorkflowID: "one"}, events.MessageUpdate, events.None, events.MessageData{Delta: text}); err != nil {
					t.Fatal(err)
				}
			}
			f.state = workflows.State{LastSeq: 10}
			m.Update(m.tailCmd()())
			if !pending {
				m.Update(m.queryCmd()())
			}
			m.switchSession("two")
			cmd := m.switchSession("one")
			m.Update(cmd())
			m.Update(m.queryCmd()())
			if strings.Count(m.viewport.GetContent(), "live") != 5 {
				t.Fatal("lost uncovered deltas on return")
			}
			m.Update(tailMsg{epoch: m.epoch, events: []streamEvent{delta(16, "next")}, at: time.Now()})
			if m.lastSeq != 16 || m.queryPending {
				t.Fatal("return caused gap")
			}
		})
	}
}

func TestClipboardIsolationAndSanitization(t *testing.T) {
	for _, failure := range []bool{false, true} {
		m, _ := testModel(t)
		started, release := make(chan struct{}), make(chan struct{})
		cmd := m.clipboardCmd(func() (string, error) {
			close(started)
			<-release
			if failure {
				return "", errors.New("old error")
			}
			return "old text", nil
		})
		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		<-started
		m.switchSession("two")
		m.prompt.SetValue("new")
		notice := m.notice
		close(release)
		m.Update(<-result)
		if m.prompt.Value() != "new" || m.notice != notice {
			t.Fatal("stale clipboard result affected new session")
		}
	}
	a, _ := testModel(t)
	b, _ := testModel(t)
	text := "ok\x1b\x00\u202e\u200b\t🙂\nend"
	a.Update(a.clipboardCmd(func() (string, error) { return text, nil })())
	b.Update(tea.PasteMsg{Content: text})
	if a.prompt.Value() != safeText(text) || a.prompt.Value() != b.prompt.Value() {
		t.Fatal("paste paths differ")
	}
	_, cmd := a.Update(key('v', tea.ModCtrl))
	if cmd == nil || a.prompt.KeyMap.Paste.Enabled() {
		t.Fatal("native clipboard path not replaced")
	}
}

func TestPromptHardBound(t *testing.T) {
	for _, path := range []string{"terminal", "clipboard", "key"} {
		t.Run(path, func(t *testing.T) {
			m, _ := testModel(t)
			m.prompt.SetValue("a")
			for i := 0; i < 5; i++ {
				text := strings.Repeat("\u0301", maxPromptRunes/2)
				switch path {
				case "terminal":
					m.Update(tea.PasteMsg{Content: text})
				case "clipboard":
					m.Update(clipboardMsg{epoch: m.epoch, text: text})
				case "key":
					m.Update(tea.KeyPressMsg{Code: 'x', Text: text})
				}
				if utf8.RuneCountInString(m.prompt.Value()) > maxPromptRunes {
					t.Fatal("unbounded combining marks")
				}
			}
			m.Update(key(tea.KeyEnter, tea.ModShift))
			if utf8.RuneCountInString(m.prompt.Value()) > maxPromptRunes {
				t.Fatal("newline exceeded bound")
			}
		})
	}
	m, _ := testModel(t)
	m.Update(tea.PasteMsg{Content: "ASCII🙂\n日本語"})
	if m.prompt.Value() != "ASCII🙂\n日本語" {
		t.Fatal("unicode/multiline lost")
	}
	m.prompt.SetValue(strings.Repeat("a", maxPromptRunes-2))
	m.prompt.CursorStart()
	m.Update(tea.PasteMsg{Content: "🙂界extra"})
	if m.prompt.Value() != "🙂界"+strings.Repeat("a", maxPromptRunes-2) {
		t.Fatal("middle insertion damaged suffix or utf8")
	}
	m.Update(key(tea.KeyBackspace, 0))
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if !strings.HasPrefix(m.prompt.Value(), "🙂b") || utf8.RuneCountInString(m.prompt.Value()) != maxPromptRunes {
		t.Fatalf("editing at cap failed: prefix=%q runes=%d", string([]rune(m.prompt.Value())[:5]), utf8.RuneCountInString(m.prompt.Value()))
	}
}

func TestDemoBannerDoesNotAutoplay(t *testing.T) {
	for _, demo := range []bool{false, true} {
		f := &fakeBackend{}
		m := New(f, Options{SessionID: "one", DataDir: t.TempDir(), Demo: demo})
		defer m.cancel()
		_, cmd := m.Update(m.tailCmd()())
		for _, work := range cmd().(tea.BatchMsg)[1:] {
			m.Update(work())
		}
		if len(f.messages) != 0 {
			t.Fatal("startup submitted without Autoplay")
		}
		m.Update(runCommand(t, m, "/demo", 0))
		if len(f.messages) != 1 || f.messages[0].WhenBusy != "followUp" || f.inputs[0].Scenario != "demo" {
			t.Fatal("explicit demo failed")
		}
	}
}
