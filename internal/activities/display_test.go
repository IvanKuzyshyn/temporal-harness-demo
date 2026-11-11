package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"syscall"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestModelDisplayFailureKeepsDurableResponse(t *testing.T) {
	for _, duringDelta := range []bool{false, true} {
		t.Run(map[bool]string{false: "end", true: "callback"}[duringDelta], func(t *testing.T) {
			a := &Activities{}
			in, dir := modelFixture(t, a)
			path, err := events.NewStore(dir).Path(in.Scope.StreamID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			var lastSuccessful uint64
			a.Model = modelFunc(func(_ context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
				calls++
				// Corrupt under the writer lock, preserving the exact last successful
				// sequence (an immediate heartbeat may or may not have won).
				f := holdStreamLock(t, path)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
				var last events.Event
				if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
					t.Fatal(err)
				}
				lastSuccessful = last.Seq
				if err := f.Truncate(0); err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteAt([]byte("invalid complete record\n"), 0); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
					t.Fatal(err)
				}
				f.Close()
				if duringDelta {
					if err := emit(llm.Delta{Text: "answer"}); err != nil {
						return llm.Response{}, err
					}
				}
				return llm.Response{Message: llm.Message{Content: "answer"}}, nil
			})
			first, err := a.LLMStream(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if first.Summary != "answer" || first.LastSeq != lastSuccessful {
				t.Fatalf("result=%+v", first)
			}
			retry, err := a.LLMStream(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || retry.Summary != "answer" || retry.LastSeq != 0 {
				t.Fatalf("calls=%d retry=%+v", calls, retry)
			}
			messages, err := ReadTranscript(dir, in.TranscriptRef)
			if err != nil || len(messages) != 2 {
				t.Fatalf("messages=%v err=%v", messages, err)
			}
		})
	}
}

func TestAppendLifecycleBestEffortAndIdempotent(t *testing.T) {
	a := &Activities{}
	in, dir := modelFixture(t, a)
	path, _ := events.NewStore(dir).Path(in.Scope.StreamID)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Both user lifecycle records contain only references, not the text.
	var records []events.Event
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var e events.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		records = append(records, e)
	}
	if len(records) != 2 || records[0].Type != events.MessageStart || records[1].Type != events.MessageEnd {
		t.Fatalf("records=%v", records)
	}
	if bytes.Contains(b, []byte("hello")) || !bytes.Contains(b, []byte(in.TranscriptRef+"#")) {
		t.Fatalf("unexpected events: %s", b)
	}
	tool := workflows.AppendInput{Scope: in.Scope, DataDir: dir, EntryID: "tool", Messages: []llm.Message{{Role: "tool", Content: "secret result"}}}
	result, err := a.AppendTranscript(context.Background(), tool)
	if err != nil || result.LastSeq != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := os.WriteFile(path, []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err = a.AppendTranscript(context.Background(), tool)
		if err != nil || result.LastSeq != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	messages, err := ReadTranscript(dir, in.TranscriptRef)
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
}
