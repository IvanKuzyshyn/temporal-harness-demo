package activities

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestDisplayLockedStoreReturnsPromptly(t *testing.T) {
	dir := t.TempDir()
	s := events.NewStore(dir)
	in := workflows.EmitInput{DataDir: dir, Scope: events.Scope{StreamID: "display", WorkflowID: "workflow", RunID: "run"}, Type: events.Heartbeat}
	if _, err := s.Append(in.Scope, in.Type, in.Pattern, in.Data); err != nil {
		t.Fatal(err)
	}
	path, _ := s.Path(in.Scope.StreamID)
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	a := &Activities{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		seq, err := a.EmitEvent(context.Background(), in)
		if seq != 0 || !errors.Is(err, events.ErrBusy) {
			t.Errorf("seq=%d err=%v", seq, err)
		}
		last := uint64(1)
		if err := a.emitDisplay(context.Background(), in, &last); err != nil || last != 1 {
			t.Errorf("last=%d err=%v", last, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		seq, err = a.EmitEvent(ctx, in)
		if seq != 0 || !errors.Is(err, context.Canceled) {
			t.Errorf("canceled seq=%d err=%v", seq, err)
		}
		if err := a.emitDisplay(ctx, in, &last); !errors.Is(err, context.Canceled) || last != 1 {
			t.Errorf("canceled last=%d err=%v", last, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("display waited on held stream lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	last := uint64(1)
	if err := a.emitDisplay(context.Background(), in, &last); err != nil || last != 2 {
		t.Fatalf("released last=%d err=%v", last, err)
	}
	es, _, err := events.Tail(path, 0)
	if err != nil || len(es) != 2 {
		t.Fatalf("events=%v err=%v", es, err)
	}
}
