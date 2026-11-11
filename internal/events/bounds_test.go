package events

import (
	"os"
	"strings"
	"testing"
)

func TestBounds(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Append(testScope(), Heartbeat, None, strings.Repeat("x", MaxRecordBytes)); err == nil {
		t.Fatal("oversized append accepted")
	}
	p, _ := s.Path("parent")
	line := `{"seq":1,"data":null}` + "\n"
	if err := os.WriteFile(p, []byte(strings.Repeat(line, MaxTailRecords+1)), 0600); err != nil {
		t.Fatal(err)
	}
	es, off, err := Tail(p, 0)
	if err != nil || len(es) != MaxTailRecords {
		t.Fatal(len(es), err)
	}
	es, _, err = Tail(p, off)
	if err != nil || len(es) != 1 {
		t.Fatal(len(es), err)
	}
	os.WriteFile(p, []byte(strings.Repeat("x", MaxRecordBytes)), 0600)
	if _, _, err := Tail(p, 0); err == nil {
		t.Fatal("oversized tail accepted")
	}
	if _, err := s.Append(testScope(), Heartbeat, None, nil); err == nil {
		t.Fatal("oversized crash fragment accepted")
	}
	r := NewReconnect()
	if err := r.Buffer(make([]Event, MaxReconnectEvents+1)); err == nil {
		t.Fatal("unbounded reconnect")
	}
}

func TestTailCompleteAfterPartial(t *testing.T) {
	p := t.TempDir() + "/stream"
	os.WriteFile(p, []byte(`{"seq":1,"data":null}`), 0600)
	if es, n, err := Tail(p, 0); len(es) != 0 || n != 0 || err != nil {
		t.Fatal(es, n, err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n")
	f.Close()
	if es, n, err := Tail(p, 0); len(es) != 1 || n == 0 || err != nil {
		t.Fatal(es, n, err)
	}
}
