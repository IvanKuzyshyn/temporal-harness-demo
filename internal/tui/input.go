package tui

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/atotto/clipboard"
)

var readClipboard = clipboard.ReadAll

type clipboardMsg struct {
	epoch uint64
	text  string
	err   error
}

func (m *Model) clipboardCmd(read func() (string, error)) tea.Cmd {
	epoch := m.epoch
	return func() tea.Msg {
		text, err := read()
		return clipboardMsg{epoch: epoch, text: text, err: err}
	}
}

// Budget by retained runes, never display cells. Truncate the insertion rather
// than resetting the textarea so edits in the middle preserve cursor and suffix.
func (m *Model) promptBudget(text string) string {
	text = safeText(text)
	remaining := max(0, maxPromptRunes-utf8.RuneCountInString(m.prompt.Value()))
	for i := range text {
		if remaining == 0 {
			return text[:i]
		}
		remaining--
	}
	return text
}

func (m *Model) insertPrompt(text string) {
	m.prompt.InsertString(m.promptBudget(text))
	m.capPrompt()
}

func (m *Model) capPrompt() {
	// Defensive bound for component editing operations (e.g. case conversion).
	text := m.prompt.Value()
	if utf8.RuneCountInString(text) > maxPromptRunes {
		m.prompt.SetValue(string([]rune(text)[:maxPromptRunes]))
	}
}

// Reconnect validates ordering only. Keep the bounded display projections in
// pending so opaque workflow keys survive resolution without changing events.
func sequenceEvents(batch []streamEvent) []events.Event {
	seqs := make([]events.Event, len(batch))
	for i, e := range batch {
		seqs[i].Seq = e.Seq
	}
	return seqs
}

func (m *Model) bufferEvents(batch []streamEvent) error {
	if err := m.reconnect.Buffer(sequenceEvents(batch)); err != nil {
		return err
	}
	m.pending = append(m.pending, batch...)
	return nil
}
