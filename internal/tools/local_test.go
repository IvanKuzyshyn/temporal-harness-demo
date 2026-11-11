package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/llm"
)

func call(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: "id", Name: name, Args: json.RawMessage(args)}
}
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestPaths(t *testing.T) {
	root := fixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../oops", "x/../../oops", outside, "escape/file", ".git/config", ".data/log"} {
		for _, write := range []bool{false, true} {
			if _, err := ResolvePath(root, p, write); err == nil {
				t.Errorf("accepted %q write=%v", p, write)
			}
		}
	}
	if _, err := ResolvePath(root, "alias", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePath(root, "new.txt", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePath(root, "new.txt", false); err == nil {
		t.Fatal("missing read accepted")
	}
	if _, err := New(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("file root accepted")
	}
	if _, err := New(""); err == nil {
		t.Fatal("empty root accepted")
	}
}
func TestCommands(t *testing.T) {
	root := fixture(t)
	os.Symlink(t.TempDir(), filepath.Join(root, "escape"))
	good := []CommandArgs{{Cmd: "go", Args: []string{"test", "./..."}}, {Cmd: "go", Args: []string{"build", "./..."}}, {Cmd: "go", Args: []string{"vet", "./..."}}, {Cmd: "ls"}, {Cmd: "cat", Args: []string{"a.txt"}}, {Cmd: "git", Args: []string{"status", "--short"}}, {Cmd: "git", Args: []string{"diff", "--no-ext-diff", "--no-textconv", "--", "a.txt"}}}
	for _, a := range good {
		if err := ValidateCommand(root, a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []CommandArgs{{Cmd: "sh", Args: []string{"-c", "ls"}}, {Cmd: "go", Args: []string{"test", "-toolexec=evil", "./..."}}, {Cmd: "go", Args: []string{"build", "-overlay=x", "./..."}}, {Cmd: "go", Args: []string{"build", "-o", "/tmp/pwn", "./..."}}, {Cmd: "git", Args: []string{"-C", "/tmp", "status"}}, {Cmd: "git", Args: []string{"-c", "x=y", "diff"}}, {Cmd: "git", Args: []string{"diff", "--ext-diff"}}, {Cmd: "ls", Args: []string{"-exec", "evil"}}, {Cmd: "cat", Args: []string{"/etc/passwd"}}, {Cmd: "cat", Args: []string{"../secret"}}, {Cmd: "cat", Args: []string{"escape/secret"}}, {Cmd: "ls", Args: []string{".; touch /tmp/pwn"}}, {Cmd: "ls", Workdir: "../"}}
	for _, a := range bad {
		if err := ValidateCommand(root, a); err == nil {
			t.Errorf("accepted %+v", a)
		}
	}
}
func TestPolicyAndJSON(t *testing.T) {
	root := fixture(t)
	cases := []struct {
		c    llm.ToolCall
		want string
	}{{call("read_file", `{"path":"a.txt"}`), "allow"}, {call("write_file", `{"path":"new.txt","content":"ok"}`), "needsApproval"}, {call("run_command", `{"cmd":"ls","args":[]}`), "needsApproval"}, {call("delegate", `{"path":".","question":"inspect"}`), "allow"}, {call("read_file", `{"path":"../x"}`), "block"}, {call("nope", `{}`), "block"}}
	for _, tc := range cases {
		if got, _ := BeforeTool(root, tc.c); got != tc.want {
			t.Errorf("%+v got %s", tc.c, got)
		}
	}
	for _, args := range []string{`null`, `[]`, `{`, `{"path":null}`, `{"path":2}`, `{"path":"a.txt","extra":1}`, `{"path":"a.txt","path":"../x"}`, `{"path":"a.txt"} {}`} {
		if err := ValidateCall(root, call("read_file", args)); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
	a, err := ActionHash(call("x", `{"z":[{"b":2,"a":1}],"a":true}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ActionHash(call("x", `{ "a":true,"z":[{"a":1,"b":2}]}`))
	if err != nil || a != b {
		t.Fatalf("hash mismatch %s %s %v", a, b, err)
	}
	c, _ := ActionHash(call("y", `{"z":[{"b":2,"a":1}],"a":true}`))
	if a == c {
		t.Fatal("name not hashed")
	}
	for _, args := range []string{`null`, `[]`, `{`, `{} {}`, `{"x":1,"x":2}`} {
		if _, err := ActionHash(call("x", args)); err == nil {
			t.Errorf("hash accepted %s", args)
		}
	}
}
func TestRegistry(t *testing.T) {
	root := fixture(t)
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Definitions("model")) != 7 || len(r.Definitions("internal")) != 7 || len(r.Definitions("stranger")) != 0 {
		t.Fatal("callers")
	}
	for _, d := range r.Definitions("model") {
		unsafe := d.Name == "write_file" || d.Name == "run_command"
		if d.NeedsApproval(nil) != unsafe {
			t.Errorf("approval %s", d.Name)
		}
		if unsafe && d.Replay != Unsafe {
			t.Fatal("unsafe replay")
		}
		if (unsafe || d.Name == "delegate") != (d.ExecutionMode == Sequential) {
			t.Fatal("mode")
		}
	}
	for _, c := range []llm.ToolCall{call("read_file", `{"path":"a.txt"}`), call("list_dir", `{"path":"."}`), call("tree", `{"path":".","depth":2}`), call("grep", `{"path":".","pattern":"hello"}`), call("write_file", `{"path":"new.txt","content":"saved"}`)} {
		res := r.Execute(context.Background(), c)
		if res.IsError || res.ToolCallID != "id" {
			t.Fatalf("%s: %+v", c.Name, res)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "new.txt"))
	if string(data) != "saved" {
		t.Fatal("write failed")
	}
	for _, c := range []llm.ToolCall{call("delegate", `{"path":".","question":"inspect"}`), call("run_command", `{"cmd":"ls","args":[]}`)} {
		res := r.Execute(context.Background(), c)
		if !res.IsError || !strings.Contains(res.Output, "unsupported") {
			t.Fatalf("%+v", res)
		}
	}
	os.WriteFile(filepath.Join(root, "big"), []byte(strings.Repeat("x", 20000)), 0600)
	if res := r.Execute(context.Background(), call("read_file", `{"path":"big"}`)); res.IsError || len(res.Output) > 8192 {
		t.Fatalf("bound: %+v", res)
	}
	os.Mkdir(filepath.Join(root, ".git"), 0700)
	os.WriteFile(filepath.Join(root, ".git", "secret"), []byte("hello"), 0600)
	os.Symlink(t.TempDir(), filepath.Join(root, "outside"))
	for _, name := range []string{"tree", "grep", "list_dir"} {
		args := `{"path":"."}`
		if name == "grep" {
			args = `{"path":".","pattern":"hello"}`
		}
		res := r.Execute(context.Background(), call(name, args))
		if res.IsError || strings.Contains(res.Output, ".git") {
			t.Fatalf("%s: %+v", name, res)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := r.Execute(ctx, call("read_file", `{"path":"a.txt"}`)); !res.IsError || !res.Interrupted {
		t.Fatalf("cancel: %+v", res)
	}
}
