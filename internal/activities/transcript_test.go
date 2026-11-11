package activities

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/mock"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
)

func TestTranscriptRetryAndModelPersistence(t *testing.T) {
	ctx := context.Background()
	a := &Activities{Model: mock.New()}
	dir := t.TempDir()
	scope := events.Scope{StreamID: "session-test", WorkflowID: "session-test", RunID: "run-1"}
	in := workflows.AppendInput{Scope: scope, DataDir: dir, EntryID: "input-1", Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	first, err := a.AppendTranscript(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.TranscriptRef = first.TranscriptRef
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := a.AppendTranscript(ctx, in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	modelIn := workflows.LLMInput{Scope: scope, DataDir: dir, TranscriptRef: first.TranscriptRef, InputID: "input-1", Turn: 0}
	res, err := a.LLMStream(ctx, modelIn)
	if err != nil {
		t.Fatal(err)
	}
	a.Model = panicModel{}
	retry, err := a.LLMStream(ctx, modelIn)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "Mock assistant: ready." || retry.MessageRef != res.MessageRef {
		t.Fatalf("results: %+v / %+v", res, retry)
	}
	messages, err := ReadTranscript(dir, first.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Content != res.Summary {
		t.Fatalf("messages: %+v", messages)
	}
	path, _ := events.NewStore(dir).Path(scope.StreamID)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "message_update") || !strings.Contains(string(b), "message_end") {
		t.Fatal(string(b))
	}
	in.Messages[0].Content = "different"
	if _, err := a.AppendTranscript(ctx, in); err == nil {
		t.Fatal("accepted conflicting entry ID")
	}
	if _, err := ReadTranscript(dir, "../../escape"); err == nil {
		t.Fatal("accepted escaping ref")
	}
}

type panicModel struct{}

func (panicModel) Chat(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
	panic("persisted response must not call model")
}
