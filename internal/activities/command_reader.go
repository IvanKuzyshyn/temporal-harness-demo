package activities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const MaxCommandLogPage = 16 << 10
const MaxCommandLogBytes = 64 << 20

type CommandLogPage struct {
	Text               string
	Offset, Next, Size int64
	HasMore, Truncated bool
}

// ReadCommandLogPage opens only a canonical ref in this exact workflow scope.
// Every directory is pinned by os.Root; nonblocking open rejects special files
// without waiting for a FIFO peer. Pages are bytes, not misleading line numbers.
// Cancellation bounds reads, not filesystem syscall latency. No arbitrary opener.
func ReadCommandLogPage(ctx context.Context, dataDir, workflowID, ref string, offset int64) (CommandLogPage, error) {
	fail := func(err error) (CommandLogPage, error) { return CommandLogPage{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(workflowID)))
	prefix := "artifacts/" + scope + "/cmd-"
	if workflowID == "" || !filepath.IsAbs(dataDir) || !strings.HasPrefix(ref, prefix) || !strings.HasSuffix(ref, ".log") || len(ref) != len(prefix)+64+4 || offset < 0 || offset >= MaxCommandLogBytes {
		return fail(fmt.Errorf("invalid command log scope, reference or cursor"))
	}
	hash := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), ".log")
	if _, err := hex.DecodeString(hash); err != nil || strings.ToLower(hash) != hash {
		return fail(fmt.Errorf("invalid command log identity"))
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	artifacts, err := pinnedLogDirectory(root, "artifacts")
	if err != nil {
		return fail(err)
	}
	defer artifacts.Close()
	session, err := pinnedLogDirectory(artifacts, scope)
	if err != nil {
		return fail(err)
	}
	defer session.Close()
	before, err := session.Lstat(filepath.Base(ref))
	if err != nil {
		return fail(err)
	}
	if !before.Mode().IsRegular() {
		return fail(fmt.Errorf("command log is not a regular non-symlink file"))
	}
	f, err := session.OpenFile(filepath.Base(ref), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !st.Mode().IsRegular() || !os.SameFile(before, st) {
		return fail(fmt.Errorf("command log is not a regular file"))
	}
	end := min(st.Size(), int64(MaxCommandLogBytes))
	if offset > end {
		return fail(fmt.Errorf("command log cursor exceeds file (artifact changed)"))
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return fail(err)
	}
	raw, err := io.ReadAll(io.LimitReader(transcriptContextReader{ctx, f}, min(int64(MaxCommandLogPage), end-offset)))
	if err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	next := offset + int64(len(raw))
	if next != min(offset+MaxCommandLogPage, end) {
		return fail(fmt.Errorf("command log changed during read"))
	}
	return CommandLogPage{Text: strings.ToValidUTF8(string(raw), "?"), Offset: offset, Next: next, Size: st.Size(), HasMore: next < end, Truncated: st.Size() > MaxCommandLogBytes}, nil
}

func pinnedLogDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("command log directory must not be a symlink")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		child.Close()
		return nil, fmt.Errorf("command log directory changed during open")
	}
	return child, nil
}
