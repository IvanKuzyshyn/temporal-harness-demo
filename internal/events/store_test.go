package events

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testScope() Scope {
	return Scope{StreamID: "parent", WorkflowID: "parent/child", RunID: "run", ParentWorkflowID: "parent"}
}
func TestStoreConcurrent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := s.Append(testScope(), Heartbeat, Child, HeartbeatData{}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	p, _ := s.Path("parent")
	es, _, err := Tail(p, 0)
	if err != nil || len(es) != 160 {
		t.Fatalf("%d %v", len(es), err)
	}
	for i, e := range es {
		if e.Seq != uint64(i+1) || e.WorkflowID != "parent/child" || e.ParentWorkflowID != "parent" {
			t.Fatalf("%+v", e)
		}
		if _, err := time.Parse(time.RFC3339Nano, e.TS); err != nil {
			t.Fatal(err)
		}
	}
}
func TestSubprocessWriter(t *testing.T) {
	if dir := os.Getenv("EVENT_STORE_TEST_DIR"); dir != "" {
		for i := 0; i < 20; i++ {
			if _, err := NewStore(dir).Append(testScope(), Heartbeat, None, nil); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	dir := t.TempDir()
	cmds := make([]*exec.Cmd, 4)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestSubprocessWriter$")
		cmds[i].Env = append(os.Environ(), "EVENT_STORE_TEST_DIR="+dir)
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := NewStore(dir).Path("parent")
	es, _, err := Tail(p, 0)
	if err != nil || len(es) != 80 {
		t.Fatalf("%d %v", len(es), err)
	}
	for i, e := range es {
		if e.Seq != uint64(i+1) {
			t.Fatal(e.Seq)
		}
	}
}
func TestRepairAndTail(t *testing.T) {
	s := NewStore(t.TempDir())
	p, _ := s.Path("parent")
	if es, n, err := Tail(p, 0); err != nil || len(es) != 0 || n != 0 {
		t.Fatal(es, n, err)
	}
	s.Append(testScope(), Heartbeat, None, nil)
	_, off, err := Tail(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":2`)
	f.Close()
	if es, n, err := Tail(p, off); err != nil || len(es) != 0 || n != off {
		t.Fatal(es, n, err)
	}
	e, err := s.Append(testScope(), Heartbeat, None, nil)
	if err != nil || e.Seq != 2 {
		t.Fatal(e, err)
	}
	es, end, err := Tail(p, off)
	if err != nil || len(es) != 1 {
		t.Fatal(es, err)
	}
	os.Truncate(p, 0)
	if _, _, err := Tail(p, end); !errors.Is(err, ErrTruncated) {
		t.Fatal(err)
	}
	f, _ = os.OpenFile(p, os.O_WRONLY, 0600)
	f.WriteString("partial")
	f.Close()
	e, err = s.Append(testScope(), Heartbeat, None, nil)
	if err != nil || e.Seq != 1 {
		t.Fatal(e, err)
	}
}
func TestCaseDistinctStreams(t *testing.T) {
	s := NewStore(t.TempDir())
	ids := []string{"Session-A", "session-a"}
	paths := make([]string, len(ids))
	for i, id := range ids {
		var err error
		paths[i], err = s.Path(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if strings.EqualFold(filepath.Base(paths[0]), filepath.Base(paths[1])) {
		t.Fatal("distinct IDs have case-fold-equivalent filenames")
	}
	// Exercise actual storage too, including case-insensitive macOS volumes.
	for _, id := range ids {
		for n := 1; n <= 2; n++ {
			e, err := s.Append(Scope{StreamID: id, WorkflowID: id}, Heartbeat, None, nil)
			if err != nil || e.Seq != uint64(n) {
				t.Fatalf("%s append %d: %+v, %v", id, n, e, err)
			}
		}
	}
	for i, path := range paths {
		es, _, err := Tail(path, 0)
		if err != nil || len(es) != 2 {
			t.Fatalf("%s: %d records, %v", path, len(es), err)
		}
		for _, e := range es {
			if e.WorkflowID != ids[i] {
				t.Fatalf("cross-stream event: %+v", e)
			}
		}
	}
}

func TestHexPathLength(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, id := range []string{strings.Repeat("a", 124), strings.Repeat("é", 62)} {
		path, err := s.Path(id)
		if err != nil || len(filepath.Base(path)) != 254 {
			t.Fatalf("maximum ID: %q, %v", path, err)
		}
		if _, err := s.Path(id + "a"); err == nil {
			t.Fatal("accepted ID exceeding 124 bytes")
		}
	}
}

func TestInvalidAndPaths(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, id := range []string{"", "../escape", "/absolute", "a/../b", "a\\b", "a\x00b"} {
		if _, err := s.Path(id); err == nil {
			t.Fatal(id)
		}
	}
	a, _ := s.Path("parent/child")
	b, _ := s.Path("parent%2Fchild")
	if a == b {
		t.Fatal("collision")
	}
	if _, err := s.Append(testScope(), Heartbeat, None, json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid data")
	}
	s.Append(testScope(), Heartbeat, None, nil)
	p, _ := s.Path("parent")
	os.WriteFile(p, []byte("not json\n"), 0600)
	if _, _, err := Tail(p, 0); err == nil {
		t.Fatal("malformed tail")
	}
	if _, err := s.Append(testScope(), Heartbeat, None, nil); err == nil {
		t.Fatal("malformed append")
	}
}
func TestReconnect(t *testing.T) {
	r := NewReconnect()
	if err := r.Buffer([]Event{{Seq: 3}, {Seq: 4}, {Seq: 5}}); err != nil {
		t.Fatal(err)
	}
	es, err := r.Resolve(4)
	if err != nil || len(es) != 1 || es[0].Seq != 5 {
		t.Fatal(es, err)
	}
	r = NewReconnect()
	r.Buffer([]Event{{Seq: 7}})
	if _, err := r.Resolve(5); !errors.Is(err, ErrGap) {
		t.Fatal(err)
	}
}
