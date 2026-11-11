package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestRetryReconnectSuffixAndDuplicateTail(t *testing.T) {
	m := New(nil, Options{SessionID: "parent"})
	defer m.Close()
	es := []streamEvent{
		retryEvent(1, events.MessageStart, "parent", "", "ref", ""),
		retryEvent(2, events.MessageUpdate, "parent", "", "ref", "oldpartial"),
		retryEvent(3, events.MessageStart, "parent", "", "ref", ""),
		retryEvent(4, events.MessageUpdate, "parent", "", "ref", "newpartial"),
	}
	m.Update(tailMsg{epoch: m.epoch, events: es, at: time.Now()})
	// Even if the replay window starts after the original start, the retry
	// replaces the reconstructed partial by reference.
	m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{LastSeq: 1}, at: time.Now()})
	m.Update(tailMsg{epoch: m.epoch, events: es, at: time.Now()})
	m.refreshViewport(false)
	view := m.viewport.View()
	if strings.Contains(view, "oldpartial") || strings.Count(view, "newpartial") != 1 || len(m.stream) != 1 {
		t.Fatalf("duplicate retry after reconnect: %s", view)
	}
}

func TestStreamWorkflowKeysDoNotAliasTruncatedLabels(t *testing.T) {
	m := New(nil, Options{SessionID: "parent"})
	defer m.Close()
	prefix := strings.Repeat("x", maxText)
	for _, ref := range []string{"ref", ""} {
		m.stream = nil
		m.newMessage = false
		m.applyEvent(retryEvent(1, events.MessageUpdate, prefix+"a", prefix+"a", ref, "first"))
		m.applyEvent(retryEvent(2, events.MessageUpdate, prefix+"b", prefix+"a", ref, "second"))
		m.applyEvent(retryEvent(3, events.MessageUpdate, prefix+"b", prefix+"b", ref, "third"))
		if len(m.stream) != 3 || m.stream[0].text != "first" || m.stream[1].text != "second" || m.stream[2].text != "third" {
			t.Fatalf("workflow/parent keys aliased for ref %q: %+v", ref, m.stream)
		}
	}
}

func TestRetryIdentityDoesNotAliasLongRefsOrParents(t *testing.T) {
	m := New(nil, Options{SessionID: "parent"})
	defer m.Close()
	prefix := strings.Repeat("x", maxText)
	for i, ref := range []string{prefix + "a", prefix + "b"} {
		m.applyEvent(retryEvent(uint64(i+1), events.MessageUpdate, "child", "parent", ref, ref[len(ref)-1:]))
	}
	m.applyEvent(retryEvent(3, events.MessageUpdate, "child", "other-parent", prefix+"a", "independent"))
	m.applyEvent(retryEvent(4, events.MessageStart, "child", "parent", prefix+"a", ""))
	if len(m.stream) != 3 || m.stream[0].text != "" || m.stream[1].text != "b" || m.stream[2].text != "independent" {
		t.Fatalf("aliased identities: %+v", m.stream)
	}
	// Non-assistant starts cannot erase an assistant attempt.
	e := retryEvent(5, events.MessageStart, "child", "other-parent", prefix+"a", "")
	e.Data = []byte(strings.Replace(string(e.Data), "assistant", "user", 1))
	m.applyEvent(e)
	if m.stream[2].text != "independent" {
		t.Fatal("user start reset assistant")
	}
}
