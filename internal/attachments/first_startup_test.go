package attachments

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Two actual processes race first lock creation, not merely two goroutines or
// constructors on a fixture whose .lock already exists. One pair per root case;
// no retry/sampling loop. All children are joined before any root cleanup.
func TestConcurrentFirstStartupProcesses(t *testing.T) {
	for _, precreated := range []bool{false, true} {
		t.Run(fmt.Sprintf("precreated=%t", precreated), func(t *testing.T) {
			parent, err := os.MkdirTemp("", "webchat-first-start-")
			require.NoError(t, err)
			t.Logf("owned first-start root parent: %s", parent)
			t.Cleanup(func() {
				if t.Failed() || os.Getenv("WEBCHAT_TEST_RETAIN") == "1" {
					t.Logf("retained: %s", parent)
					return
				}
				require.NoError(t, os.RemoveAll(parent))
			})
			root := filepath.Join(parent, "attachments")
			if precreated {
				require.NoError(t, os.Mkdir(root, 0700))
			}
			_, err = os.Lstat(filepath.Join(root, ".lock"))
			require.ErrorIs(t, err, os.ErrNotExist)
			executable, err := os.Executable()
			require.NoError(t, err)
			gateRead, gateWrite, err := os.Pipe()
			require.NoError(t, err)
			readyRead, readyWrite, err := os.Pipe()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			var children []*exec.Cmd
			var output [2]bytes.Buffer
			var waited [2]bool
			t.Cleanup(func() {
				cancel()
				gateRead.Close()
				gateWrite.Close()
				readyRead.Close()
				readyWrite.Close()
				for i, child := range children {
					if !waited[i] {
						_ = child.Wait()
					}
				}
			})
			for i := range 2 {
				child := exec.CommandContext(ctx, executable, "-test.run=^TestConcurrentFirstStartupChild$", "-test.v")
				child.Env = append(os.Environ(), "WEBCHAT_FIRST_START_CHILD_ROOT="+root)
				child.ExtraFiles = []*os.File{gateRead, readyWrite}
				child.Stdout = &output[i]
				child.Stderr = &output[i]
				require.NoError(t, child.Start())
				children = append(children, child)
			}
			require.NoError(t, gateRead.Close())
			require.NoError(t, readyWrite.Close())
			// Closing on the deadline unblocks this read even if a child never reaches
			// its barrier; CommandContext kills only our exact unreaped children.
			stopClose := context.AfterFunc(ctx, func() { _ = readyRead.Close() })
			defer stopClose()
			_, err = io.ReadFull(readyRead, make([]byte, 2))
			if err == nil {
				_, err = gateWrite.Write([]byte{1, 1})
			}
			_ = gateWrite.Close()
			if err != nil {
				cancel()
			}
			var exits [2]error
			for i, child := range children {
				exits[i] = child.Wait()
				waited[i] = true
				t.Logf("child pid=%d: %s", child.Process.Pid, output[i].String())
			}
			require.NoError(t, err, "pair readiness/release")
			for _, exit := range exits {
				require.NoError(t, exit, "concurrent constructor")
			}
			marker, err := os.ReadFile(filepath.Join(root, ".webchat-attachments-v1"))
			require.NoError(t, err)
			require.Equal(t, "webchat-attachments-v1\n", string(marker))
			lock, err := os.Lstat(filepath.Join(root, ".lock"))
			require.NoError(t, err)
			require.True(t, lock.Mode().IsRegular())
			require.Equal(t, os.FileMode(0600), lock.Mode().Perm())
		})
	}
}

func TestConcurrentFirstStartupChild(t *testing.T) {
	root := os.Getenv("WEBCHAT_FIRST_START_CHILD_ROOT")
	if root == "" {
		t.Skip("independent first-start subprocess only")
	}
	gate := os.NewFile(3, "first-start-gate")
	defer gate.Close()
	ready := os.NewFile(4, "first-start-ready")
	defer ready.Close()
	_, err := ready.Write([]byte{1})
	require.NoError(t, err)
	_, err = io.ReadFull(gate, make([]byte, 1))
	require.NoError(t, err)
	// Explicit internal adapters: no SQL, Docker, model or startup environment.
	s, err := NewStore(root, &memoryRepository{}, ContainerParser{})
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestLockPreservesExistingFile(t *testing.T) {
	s, _, _ := fixtureStore(t)
	f, err := s.root.OpenFile(".lock", os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.WriteString("existing lock inode")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	before, err := s.root.Stat(".lock")
	require.NoError(t, err)
	unlock, err := s.lock(context.Background())
	require.NoError(t, err)
	defer unlock()
	after, err := s.root.Stat(".lock")
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	content, err := s.root.ReadFile(".lock")
	require.NoError(t, err)
	require.Equal(t, "existing lock inode", string(content))
}

func TestLockCancelledWait(t *testing.T) {
	s, _, _ := fixtureStore(t)
	unlock, err := s.lock(context.Background())
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	other, err := s.lock(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, other)
}

func TestLockRejectsSymlinkEscape(t *testing.T) {
	s, _, _ := fixtureStore(t)
	outside := filepath.Join(t.TempDir(), "outside-lock")
	require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
	require.NoError(t, s.root.Remove(".lock"))
	require.NoError(t, s.root.Symlink(outside, ".lock"))
	unlock, err := s.lock(context.Background())
	require.Error(t, err)
	require.Nil(t, unlock)
	content, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(content))
}
