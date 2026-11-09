package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
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
			action := ""
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
			var beats atomic.Int32
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
			if mode == "hung" && beats.Load() == 0 {
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
