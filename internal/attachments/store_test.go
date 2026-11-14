package attachments

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Real files/validation; only the SQL boundary is replaced here. SQL and
// process-death recovery are exercised separately by integration tests.
type memoryRepository struct {
	rows map[chat.AttachmentID]chat.Attachment
	root string
	fail bool
}

func (r *memoryRepository) CheckAttachmentConversation(context.Context, chat.ConversationID) error {
	return nil
}
func (r *memoryRepository) CreateAttachment(_ context.Context, a chat.Attachment) error {
	if r.fail {
		return errors.New("metadata unavailable")
	}
	b, e := os.ReadFile(filepath.Join(r.root, a.StorageKey))
	if e != nil || int64(len(b)) != a.Size {
		return errors.New("metadata before bytes")
	}
	if old, ok := r.rows[a.ID]; ok {
		if old.Checksum != a.Checksum {
			return storage.ErrConflict
		}
		return nil
	}
	r.rows[a.ID] = a
	return nil
}
func (r *memoryRepository) MarkAttachmentReady(_ context.Context, c chat.ConversationID, id chat.AttachmentID, hash string) error {
	a := r.rows[id]
	a.Status = "ready"
	r.rows[id] = a
	return nil
}
func (r *memoryRepository) GetAttachment(_ context.Context, c chat.ConversationID, id chat.AttachmentID) (chat.Attachment, error) {
	a, ok := r.rows[id]
	if !ok || a.ConversationID != c {
		return a, storage.ErrNotFound
	}
	return a, nil
}
func fixtureStore(t *testing.T) (*Store, *memoryRepository, chat.ConversationID) {
	t.Helper()
	root := t.TempDir()
	r := &memoryRepository{rows: map[chat.AttachmentID]chat.Attachment{}, root: root}
	// Pure filesystem tests never launch an unconfigured helper. Parser gates
	// explicitly opt into the trusted setup image; zero-value parser fails closed.
	p := ContainerParser{}
	if os.Getenv("WEBCHAT_TEST_PARSERS") == "1" {
		p = realParser(t)
	}
	s, e := NewStore(root, r, p)
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, r, chat.ConversationID(uuid.NewString())
}
func TestUploadOneMiBBoundary(t *testing.T) {
	s, _, c := fixtureStore(t)
	for _, n := range []int{1048576, 1048577} {
		a, e := s.Put(context.Background(), c, strings.NewReader(strings.Repeat("a", n)), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
		if n == 1048576 {
			require.NoError(t, e)
			require.EqualValues(t, n, a.Size)
			f, e := s.Open(context.Background(), c, a.ID)
			require.NoError(t, e)
			b, e := io.ReadAll(f)
			require.NoError(t, e)
			require.NoError(t, f.Close())
			require.Len(t, b, n)
		} else {
			require.ErrorIs(t, e, ErrTooLarge)
		}
	}
}
func TestSignatureMismatch(t *testing.T) {
	s, _, c := fixtureStore(t)
	for _, v := range []struct {
		name, mime string
		b          []byte
	}{{"a.png", "image/png", []byte("not PNG")}, {"a.txt", "text/plain", []byte{0xff}}, {"a.pdf", "application/pdf", []byte("hello")}, {"a.txt", "image/png", []byte("hello")}, {"a.txt", "text/plain", []byte{'a', 0, 'b'}}} {
		_, e := s.Put(context.Background(), c, bytes.NewReader(v.b), UploadMetadata{Name: v.name, MediaType: v.mime})
		require.Error(t, e)
	}
}
func TestAttachmentOwnership(t *testing.T) {
	s, _, c := fixtureStore(t)
	a, e := s.Put(context.Background(), c, strings.NewReader("secret"), UploadMetadata{Name: "../../evil.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	require.NotContains(t, a.StorageKey, "evil")
	_, e = s.Open(context.Background(), chat.ConversationID(uuid.NewString()), a.ID)
	require.ErrorIs(t, e, storage.ErrNotFound)
	require.Equal(t, "evil.txt", a.OriginalName)
}
func TestMetadataNotReadyBeforeBytes(t *testing.T) {
	s, r, c := fixtureStore(t)
	r.fail = true
	_, e := s.Put(context.Background(), c, strings.NewReader("recover original"), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
	require.Error(t, e)
	require.Empty(t, r.rows)
	r.fail = false
	require.NoError(t, s.Recover(context.Background()))
	require.Len(t, r.rows, 1)
	for _, a := range r.rows {
		require.Equal(t, "ready", a.Status)
		f, e := s.Open(context.Background(), c, a.ID)
		require.NoError(t, e)
		b, e := io.ReadAll(f)
		require.NoError(t, e)
		f.Close()
		require.Equal(t, "recover original", string(b))
	}
	require.NoError(t, s.Recover(context.Background()))
	require.Len(t, r.rows, 1)
}
func TestOpenRejectsTamperedMetadataAndBytes(t *testing.T) {
	s, r, c := fixtureStore(t)
	a, e := s.Put(context.Background(), c, strings.NewReader("secret"), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	original := a
	for _, key := range []string{"../outside", "/etc/passwd", string(uuid.NewString()) + "/original"} {
		a.StorageKey = key
		r.rows[a.ID] = a
		_, e = s.Open(context.Background(), c, a.ID)
		require.Error(t, e)
	}
	r.rows[a.ID] = original
	require.NoError(t, os.WriteFile(filepath.Join(r.root, original.StorageKey), []byte("tamper"), 0600))
	_, e = s.Open(context.Background(), c, a.ID)
	require.Error(t, e)
}
func TestRecoveryLeavesUnownedFiles(t *testing.T) {
	s, r, _ := fixtureStore(t)
	path := filepath.Join(r.root, "foreign")
	require.NoError(t, os.Mkdir(path, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "incoming"), []byte("keep"), 0600))
	require.NoError(t, s.Recover(context.Background()))
	b, e := os.ReadFile(filepath.Join(path, "incoming"))
	require.NoError(t, e)
	require.Equal(t, "keep", string(b))
}
