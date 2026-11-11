package activities

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadCommandLogPage(t *testing.T) {
	dir := t.TempDir()
	ref := fmt.Sprintf("artifacts/%x/cmd-%s.log", sha256.Sum256([]byte("session")), strings.Repeat("a", 64))
	path := filepath.Join(dir, ref)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := strings.Repeat("first\n", 4000) + "MIDDLE-EVIDENCE\n" + strings.Repeat("last\n", 5000)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := ReadCommandLogPage(context.Background(), dir, "session", ref, 16384)
	if err != nil || !strings.Contains(page.Text, "MIDDLE-EVIDENCE") || len(page.Text) > MaxCommandLogPage || !page.HasMore {
		t.Fatalf("bad page: %+v %v", page, err)
	}
	for _, bad := range []string{"../" + ref, ref + "/../file", strings.Replace(ref, "artifacts/", "other/", 1), strings.Replace(ref, "cmd-a", "cmd-A", 1)} {
		if _, err := ReadCommandLogPage(context.Background(), dir, "session", bad, 0); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := ReadCommandLogPage(context.Background(), dir, "other", ref, 0); err == nil {
		t.Fatal("cross-session log")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadCommandLogPage(ctx, dir, "session", ref, 0); err == nil {
		t.Fatal("ignored cancellation")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Remove(path)
	os.Symlink(outside, path)
	if _, err := ReadCommandLogPage(context.Background(), dir, "session", ref, 0); err == nil {
		t.Fatal("escaping symlink")
	}
	os.Remove(path)
	// Even an in-artifacts alias must not turn this scope into another session.
	sibling := filepath.Join(dir, "artifacts", "other")
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, filepath.Base(path)), []byte("OTHER SESSION"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCommandLogPage(context.Background(), dir, "session", ref, 0); err == nil {
		t.Fatal("aliased session directory accepted")
	}
	os.Remove(filepath.Dir(path))
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCommandLogPage(context.Background(), dir, "session", ref, 0); err == nil {
		t.Fatal("FIFO accepted")
	}
	os.Remove(path)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(80 << 20)
	f.Close()
	page, err = ReadCommandLogPage(context.Background(), dir, "session", ref, MaxCommandLogBytes-MaxCommandLogPage)
	if err != nil || !page.Truncated || page.HasMore {
		t.Fatalf("missing explicit cap: %+v %v", page, err)
	}
}
