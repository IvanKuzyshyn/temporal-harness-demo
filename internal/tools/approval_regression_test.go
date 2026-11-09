package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApprovalRejectsCaseAliases(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Exact schema keys still permit harmless object reordering.
	first := call("write_file", `{"path":"a.txt","content":"replacement"}`)
	second := call("write_file", `{"content":"replacement","path":"a.txt"}`)
	h1, err := ActionHash(first)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ActionHash(second)
	if err != nil || h1 != h2 {
		t.Fatalf("equivalent calls: %s != %s, %v", h1, h2, err)
	}
	for _, c := range []struct{ name, args string }{
		{"write_file", string(first.Args)}, {"write_file", string(second.Args)},
		{"run_command", `{"cmd":"ls","args":[],"workdir":"."}`},
	} {
		if err := ValidateCall(root, call(c.name, c.args)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct{ name, args string }{
		{"write_file", `{"path":"a.txt","Path":"b.txt","content":"replacement"}`},
		{"write_file", `{"Path":"b.txt","path":"a.txt","content":"replacement"}`},
		{"write_file", `{"path":"a.txt","content":"one","Content":"two"}`},
		{"write_file", `{"path":"a.txt","Content":"two","content":"one"}`},
		{"run_command", `{"cmd":"ls","Cmd":"cat","args":["a.txt"]}`},
		{"run_command", `{"cmd":"cat","args":["a.txt"],"Args":["b.txt"]}`},
		{"run_command", `{"cmd":"ls","args":[],"Workdir":"."}`},
		{"read_file", `{"path":"a.txt","Path":"b.txt"}`},
		{"list_dir", `{"path":".","PATH":"."}`},
		{"tree", `{"path":".","Depth":1}`},
		{"grep", `{"path":".","pattern":"one","Pattern":"two"}`},
		{"delegate", `{"path":".","question":"one","Question":"two"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			c := call(tc.name, tc.args)
			if err := ValidateCall(root, c); err == nil {
				t.Error("validation accepted case alias")
			}
			if decision, _ := BeforeTool(root, c); decision != "block" {
				t.Errorf("decision = %s", decision)
			}
			if _, err := ActionHash(c); err == nil {
				t.Error("hash accepted case alias")
			}
			if result := r.Execute(context.Background(), c); !result.IsError {
				t.Error("execution accepted case alias")
			}
			for _, name := range []string{"a.txt", "b.txt"} {
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(data) != "original" {
					t.Fatalf("%s changed: %q, %v", name, data, err)
				}
			}
		})
	}
}
