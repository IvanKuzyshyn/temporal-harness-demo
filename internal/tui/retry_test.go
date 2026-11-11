package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func retryEvent(seq uint64, typ events.Type, workflow, parent, ref, text string) streamEvent {
	data, _ := json.Marshal(events.MessageData{Role: "assistant", MessageRef: ref, Delta: text})
	return compactEvent(events.Event{Seq: seq, Type: typ, WorkflowID: workflow, ParentWorkflowID: parent, Data: data})
}

func TestRetryReplacesOnlyMatchingStream(t *testing.T) {
	m := New(nil, Options{SessionID: "parent"})
	defer m.Close()
	es := []streamEvent{
		retryEvent(1, events.MessageStart, "parent", "", "turn1", ""),
		retryEvent(2, events.MessageUpdate, "parent", "", "turn1", "previous-turn"),
		retryEvent(3, events.MessageStart, "parent", "", "turn2", ""),
		retryEvent(4, events.MessageUpdate, "parent", "", "turn2", "oldpartial"),
		retryEvent(5, events.MessageStart, "child", "parent", "turn2", ""),
		retryEvent(6, events.MessageUpdate, "child", "parent", "turn2", "childpartial"),
		retryEvent(7, events.MessageStart, "parent", "", "turn2", ""),
		retryEvent(8, events.MessageUpdate, "parent", "", "turn2", "newpartial"),
		retryEvent(9, events.MessageUpdate, "child", "parent", "turn2", "-continued"),
	}
	for _, e := range es {
		m.applyEvent(e)
	}
	check := func() {
		t.Helper()
		m.refreshViewport(false)
		view := m.viewport.View()
		if strings.Contains(view, "oldpartial") || strings.Count(view, "newpartial") != 1 || !strings.Contains(view, "previous-turn") || !strings.Contains(view, "childpartial-continued") || len(m.stream) != 3 {
			t.Fatalf("incorrect retry display: %s; entries=%+v", view, m.stream)
		}
	}
	check()
	m.reconnect = nil
	for range 2 {
		m.beginQuery()
		m.Update(snapshotMsg{epoch: m.epoch, query: m.queryID, state: workflows.State{}, at: time.Now()})
		check()
	}
	oldEpoch := m.epoch
	m.switchSession("other")
	m.Update(tailMsg{epoch: oldEpoch, events: es})
	if len(m.stream) != 0 {
		t.Fatal("stale epoch changed stream")
	}
}

func TestRetryStartClearsImmediatelyAndBoundsIdentity(t *testing.T) {
	m := New(nil, Options{SessionID: "parent"})
	defer m.Close()
	ref := strings.Repeat("x", maxText)
	m.applyEvent(retryEvent(1, events.MessageStart, "parent", "", ref, ""))
	m.applyEvent(retryEvent(2, events.MessageUpdate, "parent", "", ref, "oldpartial"))
	m.applyEvent(retryEvent(3, events.MessageStart, "parent", "", ref, ""))
	m.refreshViewport(false)
	if strings.Contains(m.viewport.View(), "oldpartial") {
		t.Fatal("restart retained old partial")
	}
	e := retryEvent(4, events.MessageStart, "parent", "", ref, strings.Repeat("unused", maxText))
	var d events.MessageData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Role != "assistant" || d.MessageRef == "" || len(d.MessageRef) > 256 || d.Delta != "" {
		t.Fatalf("unbounded or missing start metadata: %.300s", e.Data)
	}
	for i := 0; i < maxEntries*2; i++ {
		ref := strings.Repeat("x", maxText) + string(rune(i))
		m.applyEvent(retryEvent(uint64(i+5), events.MessageUpdate, "parent", "", ref, "bounded"))
	}
	if len(m.stream) > maxEntries {
		t.Fatal("unbounded stream entries")
	}
}

func TestRetryEmptyRefKeepsLegacySegments(t *testing.T) {
	m := New(nil, Options{})
	defer m.Close()
	for i := 0; i < 2; i++ {
		m.applyEvent(retryEvent(uint64(i*2+1), events.MessageStart, "", "", "", ""))
		m.applyEvent(retryEvent(uint64(i*2+2), events.MessageUpdate, "", "", "", "partial"))
	}
	if len(m.stream) != 2 {
		t.Fatalf("legacy segments lost: %+v", m.stream)
	}
}
