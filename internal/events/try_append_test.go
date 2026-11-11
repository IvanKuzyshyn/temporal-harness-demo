package events

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestTryAppendLockedAndReleased(t *testing.T) {
	s := NewStore(t.TempDir())
	scope := testScope()
	if _, err := s.Append(scope, Heartbeat, Child, nil); err != nil {
		t.Fatal(err)
	}
	path, _ := s.Path(scope.StreamID)
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := flock(f, syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer flock(f, syscall.LOCK_UN)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			e, err := s.TryAppend(scope, Heartbeat, Child, nil)
			if !errors.Is(err, ErrBusy) || !reflect.DeepEqual(e, Event{}) {
				t.Errorf("event=%+v err=%v", e, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TryAppend waited on held lock")
	}
	// A normal writer must wait, not drop the event or report ErrBusy.
	normal := make(chan error, 1)
	go func() { _, err := s.Append(scope, Heartbeat, Child, nil); normal <- err }()
	select {
	case err := <-normal:
		t.Fatalf("Append returned while locked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := flock(f, syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-normal:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Append did not resume")
	}
	e, err := s.TryAppend(scope, Heartbeat, Child, HeartbeatData{})
	if err != nil || e.Seq != 3 || e.WorkflowID != scope.WorkflowID || e.RunID != scope.RunID || e.ParentWorkflowID != scope.ParentWorkflowID || e.Type != Heartbeat || e.Pattern != Child || string(e.Data) != `{"activity":"","progress":0}` {
		t.Fatalf("event=%+v err=%v", e, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.TS); err != nil {
		t.Fatal(err)
	}
	es, _, err := Tail(path, 0)
	if err != nil || len(es) != 3 {
		t.Fatalf("events=%v err=%v", es, err)
	}
}

func TestTryAppendConcurrentNormalIntegrity(t *testing.T) {
	s := NewStore(t.TempDir())
	var wg sync.WaitGroup
	successes := make(chan Event, 320)
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				var e Event
				var err error
				if worker%2 == 0 {
					e, err = s.Append(testScope(), Heartbeat, None, nil)
				} else {
					e, err = s.TryAppend(testScope(), Heartbeat, None, nil)
					if errors.Is(err, ErrBusy) {
						continue
					}
				}
				if err != nil {
					t.Error(err)
					return
				}
				successes <- e
			}
		}()
	}
	wg.Wait()
	close(successes)
	seen := map[uint64]bool{}
	for e := range successes {
		if seen[e.Seq] {
			t.Fatalf("duplicate sequence %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	path, _ := s.Path(testScope().StreamID)
	es, _, err := Tail(path, 0)
	if err != nil || len(es) != len(seen) || len(es) < 160 {
		t.Fatalf("stored=%d successful=%d err=%v", len(es), len(seen), err)
	}
	for i, e := range es {
		if e.Seq != uint64(i+1) || !seen[e.Seq] {
			t.Fatalf("lost/unexpected sequence: %+v", e)
		}
	}
}

func TestTryAppendValidationMatchesAppend(t *testing.T) {
	for _, tc := range []struct {
		name, stream, contents string
		data                   any
	}{
		{name: "invalid scope", stream: "../bad"},
		{name: "invalid payload", stream: "parent", data: json.RawMessage(`{`)},
		{name: "oversize", stream: "parent", data: strings.Repeat("x", MaxRecordBytes)},
		{name: "corrupt stream", stream: "parent", contents: "invalid\n"},
		{name: "exhausted", stream: "parent", contents: "{\"seq\":18446744073709551615,\"data\":null}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(t.TempDir())
			scope := testScope()
			scope.StreamID = tc.stream
			if tc.contents != "" {
				if _, err := s.Append(scope, Heartbeat, None, nil); err != nil {
					t.Fatal(err)
				}
				path, _ := s.Path(scope.StreamID)
				if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, want := s.Append(scope, Heartbeat, None, tc.data)
			e, got := s.TryAppend(scope, Heartbeat, None, tc.data)
			if want == nil || got == nil || want.Error() != got.Error() || !reflect.DeepEqual(e, Event{}) {
				t.Fatalf("event=%+v Append=%v TryAppend=%v", e, want, got)
			}
		})
	}
}
