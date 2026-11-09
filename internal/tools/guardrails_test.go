package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSymlinkGuards(t *testing.T) {
	root := fixture(t)
	outside := fixture(t)
	for name, target := range map[string]string{"external": filepath.Join(outside, "a.txt"), "dangling": filepath.Join(outside, "missing"), "inside": "a.txt"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, path := range []string{"external", "dangling", "../missing", "missing/child"} {
		raw, _ := json.Marshal(FileArgs{Path: path, Content: "changed"})
		res := r.Execute(context.Background(), call("write_file", string(raw)))
		if !res.IsError {
			t.Errorf("write accepted %s", path)
		}
	}
	res := r.Execute(context.Background(), call("write_file", `{"path":"inside","content":"changed"}`))
	if res.IsError {
		t.Fatal(res.Output)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "changed" {
		t.Fatal("internal symlink did not resolve")
	}
	data, _ = os.ReadFile(filepath.Join(outside, "a.txt"))
	if string(data) != "hello\nworld\n" {
		t.Fatal("outside file modified")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tool-write-") {
			t.Fatal("temporary write leaked")
		}
	}
}

func TestBoundedSortedSearch(t *testing.T) {
	root := fixture(t)
	for i := 99; i >= 0; i-- {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), []byte(strings.Repeat("match\n", 2000)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".data", ".git"} {
		os.Mkdir(filepath.Join(root, name), 0700)
		os.WriteFile(filepath.Join(root, name, "hidden"), []byte("match"), 0600)
	}
	os.Symlink(".data", filepath.Join(root, "hidden-alias"))
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, tc := range []struct{ name, args string }{{"tree", `{"path":".","depth":2}`}, {"list_dir", `{"path":"."}`}, {"grep", `{"path":".","pattern":"match"}`}} {
		first := r.Execute(context.Background(), call(tc.name, tc.args))
		second := r.Execute(context.Background(), call(tc.name, tc.args))
		if first.IsError || len(first.Output) > 8192 || first.Output != second.Output || strings.Contains(first.Output, ".data") || strings.Contains(first.Output, ".git") {
			t.Fatalf("%s: %+v", tc.name, first)
		}
		if tc.name == "grep" && first.Details["truncated"] != "true" {
			t.Fatal("expected truncation")
		}
	}
	for _, args := range []string{`{"path":".","pattern":"["}`, `{"path":"hidden-alias","pattern":"match"}`, `{"path":".","pattern":"` + strings.Repeat("x", 1025) + `"}`} {
		if res := r.Execute(context.Background(), call("grep", args)); !res.IsError {
			t.Errorf("accepted %s", args)
		}
	}
	for _, args := range []string{`{"path":".","depth":-1}`, `{"path":".","depth":33}`, `{"path":".","depth":1.5}`} {
		if err := ValidateCall(root, call("tree", args)); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
}

func TestRootPinnedAndMetadataIsolated(t *testing.T) {
	root := fixture(t)
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defs := r.Definitions("model")
	defs[0].Callers[0] = "attacker"
	defs[0].Parameters[0] = 'x'
	d, _ := r.Lookup("delegate")
	if d.Callers[0] != "model" || !json.Valid(d.Parameters) {
		t.Fatal("mutable metadata leaked")
	}
	outside := fixture(t)
	os.WriteFile(filepath.Join(outside, "a.txt"), []byte("outside"), 0600)
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(moved)
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	res := r.Execute(context.Background(), call("read_file", `{"path":"a.txt"}`))
	if !res.IsError && strings.Contains(res.Output, "outside") {
		t.Fatal("root replacement escaped pinned descriptor")
	}
}

func TestMalformedJSONNeverPanics(t *testing.T) {
	root := fixture(t)
	for _, raw := range []string{`{"args":[null],"cmd":"cat"}`, `{"args":null,"cmd":"ls"}`, `{"cmd":"ls","args":[],"workdir":null}`, `{"cmd":"ls","args":[],"other":true}`} {
		if err := ValidateCall(root, call("run_command", raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"a":` + strings.Repeat("[", 100) + `0` + strings.Repeat("]", 100) + `}`, `{"a":1e999999999}`, strings.Repeat(" ", maxArgs+1)} {
		_, _ = ActionHash(call("x", raw))
		_ = ValidateCall(root, call("read_file", raw))
	}
}

func FuzzToolArguments(f *testing.F) {
	for _, seed := range []string{`{}`, `null`, `{"path":"."}`, `{"path":"../x"}`, `{"cmd":"ls","args":[]}`} {
		f.Add(seed)
	}
	root := f.TempDir()
	f.Fuzz(func(t *testing.T, raw string) {
		for _, name := range []string{"read_file", "write_file", "list_dir", "tree", "grep", "run_command", "delegate"} {
			c := call(name, raw)
			_, _ = ActionHash(c)
			_ = ValidateCall(root, c)
			BeforeTool(root, c)
		}
	})
}
