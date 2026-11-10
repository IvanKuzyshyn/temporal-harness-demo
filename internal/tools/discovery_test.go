package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoveryExcludesHiddenAndGeneratedButExplicitReadIsSeparate(t *testing.T) {
	root := fixture(t)
	for _, name := range []string{".env", ".superpowers", "node_modules", "vendor", "dist", "build", "target", "__pycache__", "coverage", "venv"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "secret.py"), []byte("TODO SECRET_SENTINEL"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, c := range []struct{ name, args string }{{"list_dir", `{"path":"."}`}, {"tree", `{"path":".","depth":4}`}, {"grep", `{"path":".","pattern":"TODO"}`}} {
		result := r.Execute(context.Background(), call(c.name, c.args))
		if result.IsError || strings.Contains(result.Output, "secret") || strings.Contains(result.Output, "SECRET") || strings.Contains(result.Output, "node_modules") || strings.Contains(result.Output, ".env") {
			t.Fatalf("%s: %+v", c.name, result)
		}
	}
	result := r.Execute(context.Background(), call("read_file", `{"path":"node_modules/secret.py"}`))
	if result.IsError || !strings.Contains(result.Output, "SECRET_SENTINEL") {
		t.Fatal("discovery filter incorrectly changed explicit guarded reads", result)
	}
}
