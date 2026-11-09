package activities

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	w "github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

func holdStreamLock(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Both silent and delta-producing models must finish cancellation while the
// display lock is STILL held. The interceptor observes actual SDK heartbeat
// calls (not display mirrors), forwarding them to the throttled SDK transport.
func TestStreamingLockedDisplayHeartbeatCleanup(t *testing.T) {
	for _, delta := range []bool{false, true} {
		t.Run(map[bool]string{false: "silent", true: "delta"}[delta], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			beats := make(chan streamProgress, 32)
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&heartbeatObserver{beat: func(p streamProgress) { beats <- p }}}})
			a := &Activities{}
			in, dir := modelFixture(t, a)
			path, _ := events.NewStore(dir).Path(in.Scope.StreamID)
			locked := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.Model = modelFunc(func(ctx context.Context, _ llm.Request, emit func(llm.Delta) error) (llm.Response, error) {
				holdStreamLock(t, path)
				close(locked)
				if delta {
					if err := emit(llm.Delta{Text: "partial", Tokens: 1}); err != nil {
						return llm.Response{}, err
					}
				}
				<-ctx.Done()
				return llm.Response{}, ctx.Err()
			})
			run := func(activityCtx context.Context, in w.LLMInput) (w.LLMResult, error) {
				child, stop := context.WithCancel(activityCtx)
				joined := make(chan struct{})
				go func() {
					defer close(joined)
					select {
					case <-ctx.Done():
						stop()
					case <-child.Done():
					}
				}()
				defer func() { stop(); <-joined }()
				return a.LLMStream(child, in)
			}
			env.RegisterActivity(run)
			done := make(chan error, 1)
			go func() { _, err := env.ExecuteActivity(run, in); done <- err }()
			<-locked
			// Discard any initial heartbeat preceding lock acquisition.
			for len(beats) > 0 {
				<-beats
			}
			var p streamProgress
			for range 2 {
				select {
				case p = <-beats:
				case <-time.After(4 * time.Second):
					t.Fatal("actual heartbeat stalled under display lock")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !temporal.IsCanceledError(err) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup waited for display lock")
			}
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p.PartialRef)))
			want := ""
			if delta {
				want = "partial"
			}
			if err != nil || string(b) != want {
				t.Fatalf("partial %q: %v", b, err)
			}
			// On this platform an open descriptor can survive unlink, so also inspect
			// our process's descriptors to prove the attempt-owned file was closed.
			assertPartialClosed(t, filepath.Join(dir, filepath.FromSlash(p.PartialRef)))
			for len(beats) > 0 {
				<-beats
			}
			select {
			case <-beats:
				t.Fatal("heartbeat after joined cleanup")
			case <-time.After(1100 * time.Millisecond):
			}
		})
	}
}

func assertPartialClosed(t *testing.T, path string) {
	t.Helper()
	// fcntl probing is portable across the supported Unix targets, unlike /proc.
	for fd := 0; fd < 1024; fd++ {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		target := info.Sys().(*syscall.Stat_t)
		if st.Dev == target.Dev && st.Ino == target.Ino {
			t.Fatalf("partial descriptor %d still open", fd)
		}
	}
}

func TestStreamingHeartbeatThroughFinalization(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	beats := make(chan struct{}, 32)
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&heartbeatObserver{beat: func(streamProgress) { beats <- struct{}{} }}}})
	a := &Activities{}
	in, _ := modelFixture(t, a)
	path, _ := transcriptPath(in.DataDir, in.TranscriptRef)
	lock := holdStreamLock(t, path+".lock")
	returned := make(chan struct{})
	a.Model = modelFunc(func(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
		close(returned)
		return llm.Response{Message: llm.Message{Content: "done"}}, nil
	})
	env.RegisterActivity(a.LLMStream)
	done := make(chan error, 1)
	go func() { _, err := env.ExecuteActivity(a.LLMStream, in); done <- err }()
	<-returned
	for len(beats) > 0 {
		<-beats
	}
	for range 2 {
		select {
		case <-beats:
		case <-time.After(4 * time.Second):
			t.Fatal("heartbeat stopped during persistence")
		}
	}
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalization did not finish")
	}
}

func TestStreamingRejectsAmbiguousToolCallIDs(t *testing.T) {
	for _, ids := range [][]string{{""}, {" "}, {"same", "same"}, {"a", "b"}} {
		t.Run("IDs="+ids[0], func(t *testing.T) {
			a := &Activities{Model: modelFunc(func(context.Context, llm.Request, func(llm.Delta) error) (llm.Response, error) {
				var calls []llm.ToolCall
				for _, id := range ids {
					calls = append(calls, llm.ToolCall{ID: id, Name: "read_file", Args: []byte(`{}`)})
				}
				return llm.Response{Message: llm.Message{ToolCalls: calls}}, nil
			})}
			in, dir := modelFixture(t, a)
			result, err := a.LLMStream(context.Background(), in)
			valid := ids[0] == "a"
			if (err == nil) != valid {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			messages, err := ReadTranscript(dir, in.TranscriptRef)
			want := 1
			if valid {
				want = 2
			}
			if err != nil || len(messages) != want {
				t.Fatalf("persisted ambiguous response: %v %v", messages, err)
			}
			if !valid && len(result.ToolCalls) != 0 {
				t.Fatal("ambiguous calls returned for execution")
			}
		})
	}
}

// Observe SDK RecordHeartbeat calls before transport throttling, forwarding each.
type heartbeatObserver struct {
	interceptor.WorkerInterceptorBase
	beat func(streamProgress)
}

func (h *heartbeatObserver) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &heartbeatInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, beat: h.beat}
}

type heartbeatInbound struct {
	interceptor.ActivityInboundInterceptorBase
	beat func(streamProgress)
}

func (h *heartbeatInbound) Init(next interceptor.ActivityOutboundInterceptor) error {
	return h.Next.Init(&heartbeatOutbound{ActivityOutboundInterceptorBase: interceptor.ActivityOutboundInterceptorBase{Next: next}, beat: h.beat})
}

type heartbeatOutbound struct {
	interceptor.ActivityOutboundInterceptorBase
	beat func(streamProgress)
}

func (h *heartbeatOutbound) RecordHeartbeat(ctx context.Context, details ...any) {
	h.beat(details[0].(streamProgress))
	h.Next.RecordHeartbeat(ctx, details...)
}
