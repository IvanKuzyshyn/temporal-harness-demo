package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/testsuite"
)

func commandInput(t *testing.T, cmd string, args ...string) workflows.ToolInput {
	t.Helper()
	raw, _ := json.Marshal(tools.CommandArgs{Cmd: cmd, Args: args})
	return workflows.ToolInput{InputID: "input", Scope: events.Scope{StreamID: "workflow", WorkflowID: "workflow", RunID: "run"}, DataDir: t.TempDir(), Workdir: t.TempDir(), Call: llm.ToolCall{ID: "call", Name: "run_command", Args: raw}}
}
func executeCommand(t *testing.T, in workflows.ToolInput) (workflows.CommandResult, error) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	a := &Activities{}
	env.RegisterActivity(a.RunCommand)
	v, err := env.ExecuteActivity(a.RunCommand, in)
	var r workflows.CommandResult
	if err == nil {
		err = v.Get(&r)
	}
	return r, err
}
func TestCommandLogAndRecovery(t *testing.T) {
	in := commandInput(t, "cat", "output")
	text := strings.Repeat("line\n", 70) + strings.Repeat("x", 9000)
	if err := os.WriteFile(filepath.Join(in.Workdir, "output"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := executeCommand(t, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode != 0 || !r.Truncated || len(r.Tail) > 8192 {
		t.Fatalf("%+v", r)
	}
	full, err := os.ReadFile(filepath.Join(in.DataDir, r.LogRef))
	if err != nil || string(full) != text {
		t.Fatalf("log: %v", err)
	}
	if _, err = executeCommand(t, in); err == nil {
		t.Fatal("same intent executed twice")
	}
	os.Remove(filepath.Join(in.Workdir, "output"))
	recovered, err := (&Activities{}).RecoverCommand(context.Background(), in)
	if err != nil || !recovered.Interrupted || recovered.Tail != r.Tail {
		t.Fatalf("recover %+v %v", recovered, err)
	}
	if _, err = executeCommand(t, in); err == nil {
		t.Fatal("same intent executed twice")
	}
}
func TestRunCommandMiseFallback(t *testing.T) {
	in := commandInput(t, "go", "test", "./...")
	originalArgs := string(in.Call.Args)
	bin := t.TempDir()
	fakeGo := filepath.Join(t.TempDir(), "go")
	writeExecutable := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(fakeGo, `printf 'cwd=%s\n' "$PWD"
printf 'arg=%s\n' "$@"
`)
	mise := filepath.Join(bin, "mise")
	writeExecutable(mise, "exit 1\n")
	t.Setenv("PATH", bin) // Only mise is discoverable; Go is not on the parent PATH.
	if _, err := executeCommand(t, in); err == nil {
		t.Fatal("expected tool resolution failure")
	}
	_, logPath, err := commandLog(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("resolution failure created intent log: %v", err)
	}
	t.Setenv("FAKE_GO", fakeGo)
	writeExecutable(mise, `[ "$#" -eq 2 ] && [ "$1" = which ] && [ "$2" = go ] || exit 2
printf '%s\n' "$FAKE_GO"
`)
	r, err := executeCommand(t, in)
	if err != nil {
		t.Fatal(err)
	}
	wd, err := filepath.EvalSymlinks(in.Workdir)
	if err != nil {
		t.Fatal(err)
	}
	want := "cwd=" + wd + "\narg=test\narg=./...\n"
	if r.ExitCode != 0 || r.Tail != want || string(in.Call.Args) != originalArgs {
		t.Fatalf("result=%+v args=%s; want output %q", r, in.Call.Args, want)
	}
	full, err := os.ReadFile(logPath)
	if err != nil || string(full) != want {
		t.Fatalf("log=%q err=%v", full, err)
	}
	if _, err := executeCommand(t, in); err == nil {
		t.Fatal("same intent executed twice after mise resolution")
	}
	recovered, err := (&Activities{}).RecoverCommand(context.Background(), in)
	if err != nil || !recovered.Interrupted || recovered.Tail != want || recovered.LogRef != r.LogRef {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
}

func TestCommandRejectsUnsafe(t *testing.T) {
	for _, cmd := range []string{"sh", "echo"} {
		in := commandInput(t, cmd, "hello")
		if _, err := executeCommand(t, in); err == nil {
			t.Fatal("allowed", cmd)
		}
	}
	in := commandInput(t, "cat", "escape")
	os.Symlink(t.TempDir(), filepath.Join(in.Workdir, "escape"))
	if _, err := executeCommand(t, in); err == nil {
		t.Fatal("allowed escape")
	}
}
func TestCommandProcessLifecycle(t *testing.T) {
	for _, mode := range []string{"normal", "hung", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			in := commandInput(t, "go", "test", "./...")
			in.HangTimeout = 8 * time.Second
			// Keep the display lock held through activity return, recovery and
			// descendant checks; releasing it must not enable cleanup.
			store := events.NewStore(in.DataDir)
			if _, err := store.Append(in.Scope, events.Heartbeat, events.LongRunning, nil); err != nil {
				t.Fatal(err)
			}
			stream, _ := store.Path(in.Scope.StreamID)
			locked, err := os.OpenFile(stream, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer locked.Close()
			if err := syscall.Flock(int(locked.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			defer syscall.Flock(int(locked.Fd()), syscall.LOCK_UN)
			os.WriteFile(filepath.Join(in.Workdir, "go.mod"), []byte("module commandfixture\n\ngo 1.24\n"), 0600)
			body := `package fixture
import("testing";"os";"os/exec";"fmt";"time")
func TestChild(t *testing.T){
 c:=exec.Command("sleep","60");if err:=c.Start();err!=nil{t.Fatal(err)}
 os.WriteFile("child.pid",[]byte(fmt.Sprint(c.Process.Pid)),0600)
 fmt.Fprintln(os.Stdout,"stdout marker");fmt.Fprintln(os.Stderr,"stderr marker")
 MODE
 _=time.Second
 t.Fatal("intentional nonzero")
}`
			// go test buffers child stdout. Also write directly to the command
			// log to guarantee a live display attempt before silence/cancellation.
			_, logPath, err := commandLog(in)
			if err != nil {
				t.Fatal(err)
			}
			body = strings.Replace(body, " MODE", fmt.Sprintf(" f,_:=os.OpenFile(%q,os.O_WRONLY|os.O_APPEND,0600);f.WriteString(\"live output\\n\");f.Close()\n MODE", logPath), 1)
			action := "time.Sleep(3*time.Second)"
			if mode != "normal" {
				action = "time.Sleep(60*time.Second)"
			}
			body = strings.ReplaceAll(body, "MODE", action)
			os.WriteFile(filepath.Join(in.Workdir, "fixture_test.go"), []byte(body), 0600)
			// Warm compilation without running the fixture; production argv stays narrow.
			warm := exec.Command("go", "test", "-c", "-o", filepath.Join(t.TempDir(), "fixture"))
			warm.Dir = in.Workdir
			if b, e := warm.CombinedOutput(); e != nil {
				t.Fatalf("warm: %s %v", b, e)
			}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			var requests, beats atomic.Int32
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&commandHeartbeatObserver{requests: &requests}}})
			env.SetOnActivityHeartbeatListener(func(_ *activity.Info, v converter.EncodedValues) { beats.Add(1) })
			run := func(ctx context.Context, input workflows.ToolInput) (workflows.CommandResult, error) {
				if mode == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					go func() {
						for {
							select {
							case <-ctx.Done():
								return
							case <-time.After(10 * time.Millisecond):
								if _, e := os.Stat(filepath.Join(in.Workdir, "child.pid")); e == nil {
									cancel()
									return
								}
							}
						}
					}()
				}
				return (&Activities{}).RunCommand(ctx, input)
			}
			env.RegisterActivity(run)
			v, e := env.ExecuteActivity(run, in)
			if mode == "normal" {
				if e != nil {
					t.Fatal(e)
				}
				var r workflows.CommandResult
				v.Get(&r)
				if r.ExitCode == 0 || !strings.Contains(r.Tail, "stdout marker") || !strings.Contains(r.Tail, "stderr marker") {
					t.Fatal(r)
				}
			} else if e == nil {
				t.Fatal("expected interruption")
			}
			if mode != "cancel" && (requests.Load() < 2 || beats.Load() == 0) {
				t.Fatal("missing heartbeat")
			}
			b, e := os.ReadFile(filepath.Join(in.Workdir, "child.pid"))
			if e != nil {
				t.Fatal(e)
			}
			pid, _ := strconv.Atoi(string(b))
			// macOS reaps orphaned descendants asynchronously.
			deadline := time.Now().Add(3 * time.Second)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Fatal(fmt.Sprintf("child %d survived", pid))
			}
			r, e := (&Activities{}).RecoverCommand(context.Background(), in)
			if e != nil || !r.Interrupted {
				t.Fatal(r, e)
			}
		})
	}
}

func TestCommandGitIsolation(t *testing.T) {
	in := commandInput(t, "git", "status", "--short")
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = in.Workdir
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	git("init")
	os.Mkdir(filepath.Join(in.Workdir, "nested"), 0700)
	os.WriteFile(filepath.Join(in.Workdir, "outside"), []byte("hidden"), 0600)
	os.WriteFile(filepath.Join(in.Workdir, "nested", "inside"), []byte("visible"), 0600)
	raw, _ := json.Marshal(tools.CommandArgs{Cmd: "git", Args: []string{"status", "--short"}, Workdir: "nested"})
	in.Call.Args = raw
	git("config", "core.fsmonitor", "touch SHOULD_NOT_EXIST")
	t.Setenv("GIT_DIR", t.TempDir())
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "touch SHOULD_NOT_EXIST")
	r, e := executeCommand(t, in)
	if e != nil || r.ExitCode != 0 || strings.Contains(r.Tail, "outside") || !strings.Contains(r.Tail, "?? ./") {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = os.Stat(filepath.Join(in.Workdir, "SHOULD_NOT_EXIST")); !os.IsNotExist(e) {
		t.Fatal("fsmonitor executed")
	}
}

func TestCommandTailLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	var text strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&text, "line %d\n", i)
	}
	os.WriteFile(p, []byte(text.String()), 0600)
	var r workflows.CommandResult
	if e := commandTail(p, &r); e != nil {
		t.Fatal(e)
	}
	if !r.Truncated || strings.Count(r.Tail, "\n") != 50 || !strings.HasPrefix(r.Tail, "line 50\n") {
		t.Fatal(r)
	}
}

func TestCommandIdentity(t *testing.T) {
	in := commandInput(t, "ls")
	a, err := (&Activities{}).RecoverCommand(context.Background(), in)
	if err != nil || !a.Interrupted {
		t.Fatal(a, err)
	}
	in.InputID = "INPUT"
	b, _ := (&Activities{}).RecoverCommand(context.Background(), in)
	if strings.EqualFold(a.LogRef, b.LogRef) {
		t.Fatal("identity collision")
	}
}
