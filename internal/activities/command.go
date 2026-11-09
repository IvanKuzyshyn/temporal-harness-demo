package activities

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/events"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Commands run approved project code, NOT sandboxed code. In particular Go
// builds/tests may execute arbitrary code and access the worker's credentials.
// The workflow owns approval. This activity never retries an existing intent.
func commandLog(in workflows.ToolInput) (string, string, error) {
	if in.Scope.WorkflowID == "" || in.Scope.RunID == "" || in.InputID == "" || in.Call.ID == "" {
		return "", "", errors.New("command identity is required")
	}
	key, _ := json.Marshal([]string{in.Scope.RunID, in.InputID, in.Call.ID})
	ref := fmt.Sprintf("artifacts/%x/cmd-%x.log", sha256.Sum256([]byte(in.Scope.WorkflowID)), sha256.Sum256(key))
	root, err := filepath.Abs(in.DataDir)
	if err != nil {
		return "", "", err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", "", err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", "", err
	}
	defer dir.Close()
	if err = dir.MkdirAll(filepath.Dir(ref), 0700); err != nil {
		return "", "", err
	}
	path, err := tools.ResolvePath(root, ref, true)
	if err != nil {
		return "", "", err
	}
	return ref, path, nil
}

func commandTail(path string, r *workflows.CommandResult) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	start := st.Size() - 8192
	if start < 0 {
		start = 0
	}
	r.Truncated = start > 0
	b := make([]byte, st.Size()-start)
	_, err = f.ReadAt(b, start)
	if err != nil && err != io.EOF {
		return err
	}
	// A trailing newline terminates a line, rather than creating a 51st line.
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	n := 0
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			n++
			if n == 50 {
				b = b[i+1:]
				r.Truncated = true
				break
			}
		}
	}
	r.Tail = strings.ToValidUTF8(string(b), "?")
	return nil
}

func (a *Activities) RecoverCommand(_ context.Context, in workflows.ToolInput) (workflows.CommandResult, error) {
	ref, path, err := commandLog(in)
	r := workflows.CommandResult{ExitCode: -1, Interrupted: true, LogRef: ref}
	if err != nil {
		return r, err
	}
	err = commandTail(path, &r)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return r, err
}

// Ignore all inherited Git control variables, and local hooks/config that can
// launch programs. Refuse gitfiles and repositories discovered outside root.
func commandExec(ctx context.Context, root, wd string, args tools.CommandArgs) (*exec.Cmd, error) {
	argv := append([]string(nil), args.Args...)
	env := os.Environ()
	if args.Cmd == "git" {
		repo := wd
		for {
			st, e := os.Lstat(filepath.Join(repo, ".git"))
			if e == nil {
				if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
					return nil, errors.New("git directory must be a local directory")
				}
				break
			}
			if !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
			if repo == root {
				return nil, errors.New("no repository within project")
			}
			parent := filepath.Dir(repo)
			if parent == repo {
				return nil, errors.New("no local repository")
			}
			repo = parent
		}
		gitdir := filepath.Join(repo, ".git")
		clean := make([]string, 0, len(env))
		for _, v := range env {
			if !strings.HasPrefix(v, "GIT_") {
				clean = append(clean, v)
			}
		}
		env = append(clean, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		prefix := []string{"--no-pager", "--no-optional-locks", "--git-dir=" + gitdir, "--work-tree=" + repo, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "core.pager=cat", "-c", "diff.external=", "-c", "core.bare=false"}
		if argv[0] == "status" || len(argv) == 3 {
			argv = append(argv, "--", ".")
		} else if len(argv) == 4 {
			argv = append(argv, ".")
		}
		argv = append(argv[:1], append([]string{"--ignore-submodules=all"}, argv[1:]...)...)
		argv = append(prefix, argv...)
	}
	cmd := exec.CommandContext(ctx, args.Cmd, argv...)
	cmd.Dir = wd
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd, nil
}

func (a *Activities) RunCommand(ctx context.Context, in workflows.ToolInput) (r workflows.CommandResult, err error) {
	r.ExitCode = -1
	fail := func(e error) (workflows.CommandResult, error) {
		return r, temporal.NewNonRetryableApplicationError(e.Error(), "command", e, r)
	}
	if in.Call.Name != "run_command" {
		return fail(errors.New("expected run_command"))
	}
	if in.Workdir == "" || in.DataDir == "" {
		return fail(errors.New("project and data directories required"))
	}
	root, e := filepath.Abs(in.Workdir)
	if e != nil {
		return fail(e)
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return fail(e)
	}
	if e = tools.ValidateCall(root, in.Call); e != nil {
		return fail(e)
	}
	var args tools.CommandArgs
	if e = json.Unmarshal(in.Call.Args, &args); e != nil {
		return fail(e)
	}
	if e = tools.ValidateCommand(root, args); e != nil {
		return fail(e)
	}
	wd, e := tools.ResolvePath(root, args.Workdir, false)
	if e != nil {
		return fail(e)
	}
	// Explicit command paths must remain within the selected working directory.
	local := args
	local.Workdir = ""
	if e = tools.ValidateCommand(wd, local); e != nil {
		return fail(e)
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, e := commandExec(childCtx, root, wd, args)
	if e != nil {
		return fail(e)
	}
	ref, path, e := commandLog(in)
	if e != nil {
		return fail(e)
	}
	r.LogRef = ref
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return fail(e)
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return fail(e)
	}
	defer f.Close()
	cmd.Stdout = f
	cmd.Stderr = f
	if e = cmd.Start(); e != nil {
		return fail(e)
	}
	kill := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	heartbeat := time.NewTicker(2 * time.Second)
	defer heartbeat.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	timeout := in.HangTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	last := time.Now()
	var offset, observedSize int64
	var seq uint64
	// Read a bounded chunk per tick; a noisy process cannot block cancellation.
	output := func() {
		st, e := f.Stat()
		if e != nil {
			return
		}
		if st.Size() > observedSize {
			last = time.Now()
			observedSize = st.Size()
		}
		if st.Size() > offset {
			rf, e := os.Open(path)
			if e != nil {
				return
			}
			b := make([]byte, 4096)
			n, _ := rf.ReadAt(b, offset)
			rf.Close()
			if n > 0 {
				old := offset
				offset += int64(n)
				_ = a.emitDisplay(ctx, workflows.EmitInput{Scope: in.Scope, DataDir: in.DataDir, Type: events.ToolUpdate, Pattern: events.LongRunning, Data: events.ToolData{ToolCallID: in.Call.ID, Chunk: strings.ToValidUTF8(string(b[:n]), "?"), Offset: old}}, &seq)
			}
		}
	}
	var waitErr error
	var hung bool
loop:
	for {
		select {
		case waitErr = <-done:
			break loop
		case <-ctx.Done():
			r.Interrupted = true
			kill()
			waitErr = <-done
			break loop
		case <-heartbeat.C:
			st, _ := f.Stat()
			var size int64
			if st != nil {
				size = st.Size()
			}
			activity.RecordHeartbeat(ctx, struct {
				PID          int       `json:"pid"`
				LogOffset    int64     `json:"logOffset"`
				LastOutputAt time.Time `json:"lastOutputAt"`
			}{cmd.Process.Pid, size, last})
		case <-poll.C:
			output()
			if time.Since(last) >= timeout {
				hung = true
				kill()
				waitErr = <-done
				break loop
			}
		}
	}
	// Even a normally exiting leader may leave children holding output open.
	kill()
	// Flush the final snapshot as bounded display chunks, never in the result.
	if st, statErr := f.Stat(); statErr == nil {
		for offset < st.Size() && ctx.Err() == nil {
			before := offset
			output()
			if offset == before {
				break
			}
		}
	}
	if e = f.Sync(); e != nil {
		return fail(e)
	}
	if e = commandTail(path, &r); e != nil {
		return fail(e)
	}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		r.Interrupted = true
		return r, temporal.NewCanceledError(r)
	}
	if hung {
		return fail(errors.New("command hung: no output before timeout"))
	}
	var exit *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exit) {
		return fail(waitErr)
	}
	return r, nil
}
