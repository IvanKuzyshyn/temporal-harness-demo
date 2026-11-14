//go:build integration

package attachments

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type metadataBarrier struct{ Final, Temporary string }

// The regression is publication, not error handling: SIGKILL the real writer
// with an incomplete on-disk metadata file, then exec a separate recovery process.
func TestAttachmentMetadataPublicationProcessDeath(t *testing.T) {
	if role := os.Getenv("WEBCHAT_METADATA_PROCESS"); role != "" {
		r, p := attachmentDB(t)
		ctx := context.Background()
		root := os.Getenv("WEBCHAT_METADATA_ROOT")
		c := chat.ConversationID(os.Getenv("WEBCHAT_METADATA_CONVERSATION"))
		if role == "writer" {
			target := os.Getenv("WEBCHAT_METADATA_TARGET")
			metadataCreated = func(final, temporary string, f *os.File) {
				if filepath.Base(final) != target {
					return
				}
				partial := "{"
				if target == ".webchat-attachments-v1" {
					partial = "w"
				}
				_, e := f.WriteString(partial)
				require.NoError(t, e)
				require.NoError(t, f.Sync())
				require.NoError(t, json.NewEncoder(os.Stdout).Encode(metadataBarrier{final, temporary}))
				select {} // Parent SIGKILL: no Close, deferred unlink, unlock or graceful return.
			}
			s, e := NewStore(root, r, nil)
			require.NoError(t, e)
			defer s.Close()
			_, e = s.Put(ctx, c, strings.NewReader("interrupted new upload"), UploadMetadata{Name: "new.txt", MediaType: "text/plain"})
			t.Fatalf("writer never reached barrier: %v", e)
		}
		require.Equal(t, "reader", role)
		s, e := NewStore(root, r, nil)
		require.NoError(t, e, "fresh process constructor must succeed")
		defer s.Close()
		require.NoError(t, s.Recover(ctx), "fresh process recovery must succeed")
		require.NoError(t, s.Recover(ctx), "recovery is idempotent")
		originalRoot := os.Getenv("WEBCHAT_METADATA_ORIGINAL_ROOT")
		originalStore, e := NewStore(originalRoot, r, nil)
		require.NoError(t, e)
		defer originalStore.Close()
		id := chat.AttachmentID(os.Getenv("WEBCHAT_METADATA_ORIGINAL_ID"))
		original, e := originalStore.Open(ctx, c, id)
		require.NoError(t, e)
		b, e := io.ReadAll(original)
		require.NoError(t, e)
		require.NoError(t, original.Close())
		require.Equal(t, "previous committed original", string(b))
		var count int
		require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM attachments WHERE conversation_id=$1 AND status='ready'", c).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM attachments WHERE conversation_id=$1", c).Scan(&count))
		require.Equal(t, 1, count, "interrupted upload must not create metadata")
		return
	}
	for _, tc := range []struct {
		name, target string
		freshRoot    bool
	}{{"initial-marker", ".webchat-attachments-v1", true}, {"marker-with-earlier-original", ".webchat-attachments-v1", false}, {"owner", "owner.json", false}, {"manifest", "manifest.json", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := attachmentDB(t)
			ctx := context.Background()
			c, e := r.CreateConversation(ctx, "qwen3.5:9b")
			require.NoError(t, e)
			originalRoot := t.TempDir()
			s, e := NewStore(originalRoot, r, nil)
			require.NoError(t, e)
			original, e := s.Put(ctx, c.ID, strings.NewReader("previous committed original"), UploadMetadata{Name: "earlier.txt", MediaType: "text/plain"})
			require.NoError(t, e)
			require.NoError(t, s.Close())
			root := originalRoot
			if tc.freshRoot {
				root = t.TempDir()
			} else if tc.target == ".webchat-attachments-v1" {
				require.NoError(t, os.Remove(filepath.Join(root, tc.target)))
			} // Fixture: exercise marker publication with preexisting content too.
			// Neither a layout marker nor temporary-looking names confer ownership.
			unrelated := filepath.Join(root, ".webchat-attachments-v1.pending-"+uuid.NewString())
			require.NoError(t, os.WriteFile(unrelated, []byte("unowned metadata-looking file"), 0600))
			foreignDir := filepath.Join(root, uuid.NewString())
			require.NoError(t, os.Mkdir(foreignDir, 0700))
			foreignOriginal := filepath.Join(foreignDir, "original")
			require.NoError(t, os.WriteFile(foreignOriginal, []byte("unowned original"), 0600))
			env := append(os.Environ(), "WEBCHAT_METADATA_ROOT="+root, "WEBCHAT_METADATA_ORIGINAL_ROOT="+originalRoot, "WEBCHAT_METADATA_ORIGINAL_ID="+string(original.ID), "WEBCHAT_METADATA_CONVERSATION="+string(c.ID), "WEBCHAT_METADATA_TARGET="+tc.target)
			deadline, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			writer := exec.CommandContext(deadline, os.Args[0], "-test.run=^TestAttachmentMetadataPublicationProcessDeath$", "-test.timeout=20s")
			writer.Env = append(env, "WEBCHAT_METADATA_PROCESS=writer")
			stdout, e := writer.StdoutPipe()
			require.NoError(t, e)
			require.NoError(t, writer.Start())
			defer func() {
				if writer.ProcessState == nil {
					_ = writer.Process.Kill()
					_ = writer.Wait()
				}
			}()
			var barrier metadataBarrier
			require.NoError(t, json.NewDecoder(stdout).Decode(&barrier))
			require.Equal(t, tc.target, filepath.Base(barrier.Final))
			partial, e := os.ReadFile(filepath.Join(root, barrier.Temporary))
			require.NoError(t, e)
			require.Len(t, partial, 1, "writer paused before metadata completion")
			// Unknown files even inside a verified object directory must survive cleanup.
			var objectSentinel string
			if tc.target == "manifest.json" {
				objectSentinel = filepath.Join(root, filepath.Dir(barrier.Final), "unowned-note")
				require.NoError(t, os.WriteFile(objectSentinel, []byte("leave me"), 0600))
			}
			require.NoError(t, writer.Process.Kill())
			e = writer.Wait()
			var killed *exec.ExitError
			require.ErrorAs(t, e, &killed)
			require.Equal(t, -1, killed.ExitCode(), "writer was physically killed, not a graceful exit")
			reader := exec.CommandContext(deadline, os.Args[0], "-test.run=^TestAttachmentMetadataPublicationProcessDeath$", "-test.timeout=20s")
			reader.Env = append(env, "WEBCHAT_METADATA_PROCESS=reader")
			output, e := reader.CombinedOutput()
			require.NoError(t, e, "fresh-process recovery: %s", output)
			b, e := os.ReadFile(unrelated)
			require.NoError(t, e)
			require.Equal(t, "unowned metadata-looking file", string(b))
			b, e = os.ReadFile(foreignOriginal)
			require.NoError(t, e)
			require.Equal(t, "unowned original", string(b))
			if tc.target == "manifest.json" {
				require.NoFileExists(t, filepath.Join(root, barrier.Final))
				require.NoFileExists(t, filepath.Join(root, barrier.Temporary))
				require.NoFileExists(t, filepath.Join(root, filepath.Dir(barrier.Final), "incoming"))
				b, e = os.ReadFile(objectSentinel)
				require.NoError(t, e)
				require.Equal(t, "leave me", string(b))
			} else if tc.target == "owner.json" {
				require.NoFileExists(t, filepath.Join(root, barrier.Final), "partial owner must never be published")
				require.FileExists(t, filepath.Join(root, barrier.Temporary), "without committed ownership, preserve rather than guess")
			} else {
				require.FileExists(t, filepath.Join(root, barrier.Temporary), "root marker grants no cleanup authority")
			}
			t.Logf("SIGKILL writer pid=%d at %s; independent recovery pid=%d preserved prior original/SQL and unrelated files", writer.Process.Pid, tc.target, reader.Process.Pid)
		})
	}
}
