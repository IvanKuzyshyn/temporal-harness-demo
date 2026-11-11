package activities

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

type modelFunc func(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error)

func (f modelFunc) Chat(ctx context.Context, r llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
	return f(ctx, r, emit)
}

func modelFixture(t *testing.T, a *Activities) (workflows.LLMInput, string) {
	t.Helper()
	dir := t.TempDir()
	scope := events.Scope{StreamID: "session-model", WorkflowID: "session-model", RunID: "run"}
	res, err := a.AppendTranscript(context.Background(), workflows.AppendInput{Scope: scope, DataDir: dir, EntryID: "user", Messages: []llm.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	return workflows.LLMInput{Scope: scope, DataDir: dir, TranscriptRef: res.TranscriptRef, InputID: "input"}, dir
}

func TestModelConcurrentRetryOnlyPersistsOneResponse(t *testing.T) {
	a := &Activities{Model: mock.New()}
	in, dir := modelFixture(t, a)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := a.LLMStream(context.Background(), in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	messages, err := ReadTranscript(dir, in.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("duplicate response: %d", len(messages))
	}
	// A different Temporal run may reuse the input key without aliasing entries.
	in.Scope.RunID = "next-run"
	if _, err := a.LLMStream(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	messages, err = ReadTranscript(dir, in.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("run identity collision: %d", len(messages))
	}
}

func TestModelBoundsAndCancellation(t *testing.T) {
	content := strings.Repeat("界", 10000)
	a := &Activities{Model: modelFunc(func(_ context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
		if err := emit(llm.Delta{Text: content}); err != nil {
			return llm.Response{}, err
		}
		return llm.Response{Message: llm.Message{Content: content}}, nil
	})}
	in, dir := modelFixture(t, a)
	res, err := a.LLMStream(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Summary) > MaxSummaryBytes || !utf8.ValidString(res.Summary) {
		t.Fatal("unbounded or malformed summary")
	}
	messages, err := ReadTranscript(dir, in.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content != content {
		t.Fatal("full response not persisted")
	}
	in.InputID = "canceled"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = a.LLMStream(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	messages, err = ReadTranscript(dir, in.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatal("canceled response appended")
	}
}

func TestTranscriptIdentityAndAtomicBatch(t *testing.T) {
	a := &Activities{Model: mock.New()}
	in, dir := modelFixture(t, a)
	appendIn := workflows.AppendInput{Scope: in.Scope, DataDir: dir, TranscriptRef: in.TranscriptRef, EntryID: "user", Messages: []llm.Message{{Role: "assistant", Content: "new"}, {Role: "user", Content: "new"}}}
	// Seed index 1, then collide after tentative index 0 insertion. Neither the
	// new index 0 nor any other partial batch may survive the conflict.
	if _, err := a.AppendTranscript(context.Background(), appendIn); err != nil {
		t.Fatal(err)
	}
	appendIn.Messages[0].Role = "system"
	appendIn.Messages[1].Content = "conflict"
	if _, err := a.AppendTranscript(context.Background(), appendIn); err == nil {
		t.Fatal("expected conflict")
	}
	messages, err := ReadTranscript(dir, in.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("partial commit: %+v", messages)
	}
	appendIn.Scope.WorkflowID = "different"
	if _, err := a.AppendTranscript(context.Background(), appendIn); err == nil {
		t.Fatal("cross-session reference accepted")
	}
	if TranscriptRef("Session") == TranscriptRef("session") {
		t.Fatal("case-folded identity")
	}
}
