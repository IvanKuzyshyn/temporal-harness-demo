// Package attachments owns immutable originals outside SQL. Display metadata
// is never a path. All operations are confined by os.Root to a dedicated root.
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const MaxBytes = 1048576

var ErrTooLarge = errors.New("attachment exceeds 1048576 bytes")
var ErrInvalid = errors.New("invalid or unsupported attachment")

// metadataCreated is an internal filesystem fault barrier, nil in production.
// Subprocess tests pause here and are killed by their parent, without defers.
var metadataCreated func(final, temporary string, file *os.File)

type UploadMetadata struct{ Name, MediaType string }
type Repository interface {
	CheckAttachmentConversation(context.Context, chat.ConversationID) error
	CreateAttachment(context.Context, chat.Attachment) error
	MarkAttachmentReady(context.Context, chat.ConversationID, chat.AttachmentID, string) error
	GetAttachment(context.Context, chat.ConversationID, chat.AttachmentID) (chat.Attachment, error)
}
type Parser interface {
	Parse(context.Context, []byte, string, bool) (Document, error)
}
type Document = chat.ExtractedDocument
type Store struct {
	root   *os.Root
	repo   Repository
	parser Parser
}

func NewStore(path string, r Repository, p Parser) (*Store, error) {
	if !filepath.IsAbs(path) || r == nil {
		return nil, ErrInvalid
	}
	if e := os.MkdirAll(path, 0700); e != nil {
		return nil, e
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return nil, e
	}
	s := &Store{root: root, repo: r, parser: p}
	if p == nil {
		s.parser = ContainerParser{}
	}
	// API and worker may start concurrently. Serialize layout publication too;
	// an O_EXCL marker is not readable until its contents are written/fsynced.
	startup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	unlock, e := s.lock(startup)
	if e != nil {
		root.Close()
		return nil, e
	}
	defer unlock()
	// A version marker identifies this application's layout, not a cleanup license.
	// Interrupted initial publications have unique temporary names. Without a
	// committed owner, leave them alone; they cannot obstruct the next startup.
	e = s.write(".webchat-attachments-v1", []byte("webchat-attachments-v1\n"), uuid.NewString())
	if errors.Is(e, os.ErrExist) {
		var b []byte
		b, e = root.ReadFile(".webchat-attachments-v1")
		if e == nil && string(b) != "webchat-attachments-v1\n" {
			e = ErrInvalid
		}
		if e == nil {
			// Also complete a publisher interrupted after link but before dir fsync.
			e = s.syncDir(".")
		}
	}
	if e != nil {
		root.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.root.Close() }

// Advisory cross-process lock serializes mutation/recovery, including streams.
// Open uses immutable finalized objects and never needs the writer lock.
func (s *Store) lock(ctx context.Context) (func(), error) {
	f, e := s.root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	for {
		if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e == nil {
			return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
		}
		if e != unix.EWOULDBLOCK {
			f.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (s *Store) syncDir(path string) error {
	f, e := s.root.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

// write publishes immutable metadata under the writer lock. Link is an atomic
// no-replace publication (unlike Rename, which can overwrite an existing name).
// The temporary and final names are siblings on the same filesystem. A final
// name is never visible until its entire contents have been written and fsynced.
func (s *Store) write(path string, b []byte, publicationID string) error {
	if !chat.ValidID(publicationID) {
		return ErrInvalid
	}
	temporary := path + ".pending-" + publicationID
	f, e := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	// Only this successful O_EXCL grants permission to unlink this temporary.
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = s.root.Remove(temporary)
		}
	}()
	if metadataCreated != nil {
		metadataCreated(path, temporary, f)
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = s.root.Link(temporary, path); e != nil {
		return e
	}
	if e = s.syncDir(filepath.Dir(path)); e != nil {
		return e
	}
	if e = s.root.Remove(temporary); e != nil {
		return e
	}
	removeTemporary = false
	return s.syncDir(filepath.Dir(path))
}

type owner struct {
	ID            chat.AttachmentID
	Conversation  chat.ConversationID
	PublicationID string `json:",omitempty"` // Absent in original v1 owners.
}

// A committed owner explicitly identifies only its two metadata temporaries.
// Never glob temporary-looking names or infer ownership from the layout marker.
func (s *Store) removePendingMetadata(dir string, o owner) error {
	if o.PublicationID == "" {
		return nil // Older, already-published owners remain recoverable.
	}
	if !chat.ValidID(o.PublicationID) {
		return ErrInvalid
	}
	for _, name := range []string{"owner.json", "manifest.json"} {
		e := s.root.Remove(dir + "/" + name + ".pending-" + o.PublicationID)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return s.syncDir(dir)
}

func (s *Store) Put(ctx context.Context, c chat.ConversationID, in io.Reader, m UploadMetadata) (chat.Attachment, error) {
	var a chat.Attachment
	if !chat.ValidID(string(c)) {
		return a, ErrInvalid
	}
	if e := s.repo.CheckAttachmentConversation(ctx, c); e != nil {
		return a, e
	}
	name := filepath.Base(strings.ReplaceAll(m.Name, "\\", "/"))
	if name == "." || name == "/" || len(name) > 255 || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return a, ErrInvalid
	}
	unlock, e := s.lock(ctx)
	if e != nil {
		return a, e
	}
	defer unlock()
	id := chat.AttachmentID(uuid.NewString())
	dir := string(id)
	if e = s.root.Mkdir(dir, 0700); e != nil {
		return a, e
	}
	ownership := owner{ID: id, Conversation: c, PublicationID: uuid.NewString()}
	o, _ := json.Marshal(ownership)
	if e = s.write(dir+"/owner.json", o, ownership.PublicationID); e != nil {
		return a, e
	}
	if e = s.syncDir(dir); e != nil {
		return a, e
	}
	if e = s.syncDir("."); e != nil {
		return a, e
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.root.Remove(dir + "/incoming")
			_ = s.root.Remove(dir + "/manifest.json")
			_ = s.root.Remove(dir + "/owner.json")
			_ = s.root.Remove(dir)
		}
	}()
	f, e := s.root.OpenFile(dir+"/incoming", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return a, e
	}
	// Stream at most one byte past the limit; never buffer an arbitrary request.
	n, e := io.Copy(f, io.LimitReader(&contextReader{ctx, in}, MaxBytes+1))
	if e == nil && n > MaxBytes {
		e = ErrTooLarge
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return a, e
	}
	b, e := s.root.ReadFile(dir + "/incoming")
	if e != nil {
		return a, e
	}
	media, e := s.validate(ctx, b, UploadMetadata{name, m.MediaType})
	if e != nil {
		return a, e
	}
	h := sha256.Sum256(b)
	a = chat.Attachment{ID: id, ConversationID: c, OriginalName: name, MediaType: media, Size: n, Checksum: hex.EncodeToString(h[:]), StorageKey: dir + "/original", Status: "staged"}
	manifest, _ := json.Marshal(a)
	if e = s.write(dir+"/manifest.json", manifest, ownership.PublicationID); e != nil {
		return a, e
	}
	if e = s.syncDir(dir); e != nil {
		return a, e
	}
	if e = s.root.Rename(dir+"/incoming", a.StorageKey); e != nil {
		return a, e
	}
	keep = true // From here only recovery may finish; NEVER delete finalized originals.
	if e = s.syncDir(dir); e != nil {
		return a, e
	}
	if e = s.publish(ctx, a); e != nil {
		return a, e
	}
	a.Status = "ready"
	return a, nil
}
func (s *Store) publish(ctx context.Context, a chat.Attachment) error {
	if e := s.repo.CreateAttachment(ctx, a); e != nil {
		return e
	}
	return s.repo.MarkAttachmentReady(ctx, a.ConversationID, a.ID, a.Checksum)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func validObject(a chat.Attachment) bool {
	return chat.ValidID(string(a.ID)) && chat.ValidID(string(a.ConversationID)) && chat.ValidHash(a.Checksum) && a.StorageKey == string(a.ID)+"/original" && a.Size >= 0 && a.Size <= MaxBytes
}
func (s *Store) readOriginal(a chat.Attachment) ([]byte, error) {
	if !validObject(a) {
		return nil, ErrInvalid
	}
	for _, p := range []string{string(a.ID), a.StorageKey} {
		st, e := s.root.Lstat(p)
		if e != nil {
			return nil, e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
	}
	f, e := s.root.Open(a.StorageKey)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() != a.Size {
		return nil, ErrInvalid
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != a.Checksum || int64(len(b)) != a.Size {
		return nil, ErrInvalid
	}
	return b, nil
}
func (s *Store) Open(ctx context.Context, c chat.ConversationID, id chat.AttachmentID) (io.ReadCloser, error) {
	a, e := s.repo.GetAttachment(ctx, c, id)
	if e != nil {
		return nil, e
	}
	if a.ConversationID != c || a.ID != id || a.Status != "ready" {
		return nil, ErrInvalid
	}
	b, e := s.readOriginal(a)
	if e != nil {
		return nil, e
	}
	return io.NopCloser(bytes.NewReader(b)), ctx.Err()
}
func (s *Store) Recover(ctx context.Context) error {
	unlock, e := s.lock(ctx)
	if e != nil {
		return e
	}
	defer unlock()
	f, e := s.root.Open(".")
	if e != nil {
		return e
	}
	entries, e := f.ReadDir(-1)
	f.Close()
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if e = ctx.Err(); e != nil {
			return e
		}
		dir := entry.Name()
		if !chat.ValidID(dir) || !entry.IsDir() {
			continue
		}
		raw, e := s.root.ReadFile(dir + "/owner.json")
		var o owner
		if e != nil || json.Unmarshal(raw, &o) != nil || string(o.ID) != dir || !chat.ValidID(string(o.Conversation)) {
			continue
		}
		if e = s.removePendingMetadata(dir, o); e != nil {
			return fmt.Errorf("recover %s: %w", dir, e)
		}
		raw, e = s.root.ReadFile(dir + "/manifest.json")
		if errors.Is(e, os.ErrNotExist) { // Owned incomplete transfer, no finalized bytes.
			if _, e = s.root.Lstat(dir + "/original"); !errors.Is(e, os.ErrNotExist) {
				continue
			}
			_ = s.root.Remove(dir + "/incoming")
			_ = s.root.Remove(dir + "/owner.json")
			_ = s.root.Remove(dir)
			continue
		}
		if e != nil {
			return e
		}
		var a chat.Attachment
		if json.Unmarshal(raw, &a) != nil || !validObject(a) || a.ID != o.ID || a.ConversationID != o.Conversation || a.Status != "staged" {
			return fmt.Errorf("recover %s: %w", dir, ErrInvalid)
		}
		// A crash after manifest fsync but before rename is also recoverable.
		if _, e = s.root.Lstat(a.StorageKey); errors.Is(e, os.ErrNotExist) {
			if e = s.root.Rename(dir+"/incoming", a.StorageKey); e != nil {
				return e
			}
			if e = s.syncDir(dir); e != nil {
				return e
			}
		}
		b, e := s.readOriginal(a)
		if e != nil {
			return e
		}
		if _, e = s.validate(ctx, b, UploadMetadata{a.OriginalName, a.MediaType}); e != nil {
			return e
		}
		if e = s.publish(ctx, a); e != nil {
			return e
		}
	}
	return nil
}
