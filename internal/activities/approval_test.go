package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestApprovalNotification(t *testing.T) {
	a := &Activities{}
	in := workflows.EmitInput{DataDir: t.TempDir(), Scope: events.Scope{StreamID: "session", WorkflowID: "session"}, Type: events.ApprovalRequested, Pattern: events.Approval, Data: events.ApprovalData{RequestID: "request", ActionHash: "hash", Command: "secret command"}}
	var out bytes.Buffer
	calls := 0
	err := a.notifyApproval(context.Background(), in, &out, true, "linux", func(name string) (string, error) { return "/bin/" + name, nil }, func(_ context.Context, name string, args ...string) error {
		calls++
		if name != "/bin/notify-send" || strings.Contains(strings.Join(args, " "), "secret") {
			t.Errorf("notification leaked action: %s %v", name, args)
		}
		return errors.New("no desktop")
	})
	if err != nil || calls != 1 || !strings.Contains(out.String(), "\a") || strings.Contains(out.String(), "secret") {
		t.Fatalf("notification=%q calls=%d err=%v", out.String(), calls, err)
	}
	files, err := filepath.Glob(filepath.Join(in.DataDir, "streams", "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("event not emitted: %v %v", files, err)
	}
	batch, _, err := events.Tail(files[0], 0)
	if err != nil || len(batch) != 1 || batch[0].Type != events.ApprovalRequested || batch[0].Pattern != events.Approval {
		t.Fatalf("events=%+v err=%v", batch, err)
	}
	var data events.ApprovalData
	if err := json.Unmarshal(batch[0].Data, &data); err != nil || data.RequestID != "request" || data.ActionHash != "hash" {
		t.Fatalf("data=%+v err=%v", data, err)
	}
}

func TestApprovalNotificationBestEffort(t *testing.T) {
	a := &Activities{}
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	in := workflows.EmitInput{DataDir: bad, Scope: events.Scope{StreamID: "session"}, Type: events.ApprovalRequested, Pattern: events.Approval}
	var out bytes.Buffer
	lookup := func(string) (string, error) { return "", os.ErrNotExist }
	run := func(context.Context, string, ...string) error { t.Error("absent desktop invoked"); return nil }
	if err := a.notifyApproval(context.Background(), in, &out, true, "darwin", lookup, run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	if err := a.notifyApproval(ctx, in, &out, true, "linux", lookup, run); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("canceled=%v output=%q", err, out.String())
	}
}
