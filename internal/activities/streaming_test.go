package activities

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm/ollama"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestStopSettlesAborted(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	release := make(chan struct{})
	env.RegisterActivityWithOptions(func(context.Context, w.EmitInput) (uint64, error) { return 1, nil }, activity.RegisterOptions{Name: w.EmitActivity})
	env.RegisterActivityWithOptions(func(context.Context, w.AppendInput) (w.AppendResult, error) {
		return w.AppendResult{TranscriptRef: "ref"}, nil
	}, activity.RegisterOptions{Name: w.TranscriptActivity})
	env.RegisterActivityWithOptions(func(ctx context.Context, _ w.LLMInput) (w.LLMResult, error) {
		info := activity.GetInfo(ctx)
		if info.HeartbeatTimeout != 15*time.Second {
			t.Errorf("heartbeat timeout: %v", info.HeartbeatTimeout)
		}
		// The pinned testsuite immediately completes canceled activities without
		// canceling their Go contexts (internal_workflow_testsuite.go:1257).
		<-release
		return w.LLMResult{}, temporal.NewCanceledError()
	}, activity.RegisterOptions{Name: w.LLMActivity})
	env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.MessageUpdate, "message", t, w.MessageInput{Text: "hello"}) }, 0)
	env.RegisterDelayedCallback(func() { env.UpdateWorkflowNoRejection(w.StopUpdate, "stop", t) }, time.Second)
	env.RegisterDelayedCallback(func() {
		value, err := env.QueryWorkflow(w.StateQuery)
		if err != nil {
			t.Fatal(err)
		}
		var state w.State
		if err = value.Get(&state); err != nil {
			t.Fatal(err)
		}
		if state.Status != "idle" || state.LastRunFailure == nil || state.LastRunFailure.Reason != "aborted" {
			t.Errorf("state: %+v", state)
		}
		close(release)
		env.CancelWorkflow()
	}, 2*time.Second)
	env.ExecuteWorkflow(w.SessionWorkflow, w.Input{})
}

func TestStreamingCancellationRetainsPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Activities{Model: modelFunc(func(ctx context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
		if err := emit(llm.Delta{Text: "partial", Tokens: 2}); err != nil {
			return llm.Response{}, err
		}
		cancel()
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})}
	in, dir := modelFixture(t, a)
	_, err := a.LLMStream(ctx, in)
	if !temporal.IsCanceledError(err) {
		t.Fatalf("not Temporal canceled: %v", err)
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "artifacts", "*", "*.partial"))
	if len(paths) != 1 {
		t.Fatalf("partials: %v", paths)
	}
	b, err := os.ReadFile(paths[0])
	if err != nil || string(b) != "partial" {
		t.Fatalf("partial %q: %v", b, err)
	}
}

func TestBlockedModelRecordsHeartbeatDetails(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	beats := make(chan streamProgress, 8)
	env.SetOnActivityHeartbeatListener(func(_ *activity.Info, values converter.EncodedValues) {
		var p streamProgress
		if err := values.Get(&p); err != nil {
			t.Error(err)
			return
		}
		beats <- p
	})
	a := &Activities{Model: modelFunc(func(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
		select {
		case p := <-beats:
			if p.Tokens != 0 || p.PartialRef == "" {
				t.Errorf("details: %+v", p)
			}
		case <-time.After(2 * time.Second):
			t.Error("blocked model received no heartbeat")
		}
		return llm.Response{Message: llm.Message{Content: "done"}}, nil
	})}
	in, _ := modelFixture(t, a)
	env.RegisterActivity(a.LLMStream)
	if _, err := env.ExecuteActivity(a.LLMStream, in); err != nil {
		t.Fatal(err)
	}
}

func TestStreamingTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		err error
		typ string
	}{{&ollama.ModelNotFoundError{Err: errors.New("missing")}, "ModelNotFound"}, {&ollama.InvalidRequestError{Err: errors.New("bad")}, "InvalidRequest"}} {
		t.Run(tc.typ, func(t *testing.T) {
			a := &Activities{Model: modelFunc(func(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
				return llm.Response{}, tc.err
			})}
			in, _ := modelFixture(t, a)
			_, err := a.LLMStream(context.Background(), in)
			var app *temporal.ApplicationError
			if !errors.As(err, &app) || !app.NonRetryable() || app.Type() != tc.typ {
				t.Fatalf("classification: %v", err)
			}
		})
	}
}

func TestFailedAttemptsKeepIsolatedPartials(t *testing.T) {
	a := &Activities{Model: modelFunc(func(_ context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
		if err := emit(llm.Delta{Text: "unfinished"}); err != nil {
			return llm.Response{}, err
		}
		return llm.Response{}, errors.New("transport failed")
	})}
	in, dir := modelFixture(t, a)
	for range 2 {
		if _, err := a.LLMStream(context.Background(), in); err == nil {
			t.Fatal("expected model error")
		}
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "artifacts", "*", "*.partial"))
	if len(paths) != 2 {
		t.Fatalf("attempts shared partial: %v", paths)
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "unfinished" {
			t.Fatalf("partial %q: %v", b, err)
		}
	}
	messages, err := ReadTranscript(dir, in.TranscriptRef)
	if err != nil || len(messages) != 1 {
		t.Fatalf("failed response committed: %v %v", messages, err)
	}
}

func TestHeartbeatWithoutDeltasAndCleanup(t *testing.T) {
	beats := make(chan struct{}, 10)
	stop := startStreamHeartbeats(context.Background(), func() { beats <- struct{}{} })
	select {
	case <-beats:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("no idle heartbeat")
	}
	select {
	case <-beats:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("no periodic heartbeat")
	}
	stop()
	for len(beats) > 0 {
		<-beats
	}
	select {
	case <-beats:
		t.Fatal("heartbeat leaked after cleanup")
	case <-time.After(1100 * time.Millisecond):
	}
}
