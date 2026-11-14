package attachments

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMetadataPublicationNeverOverwrites(t *testing.T) {
	s, _, _ := fixtureStore(t)
	require.NoError(t, s.write("identity.json", []byte("original identity"), uuid.NewString()))
	require.ErrorIs(t, s.write("identity.json", []byte("changed identity"), uuid.NewString()), os.ErrExist)
	b, e := s.root.ReadFile("identity.json")
	require.NoError(t, e)
	require.Equal(t, "original identity", string(b))
	token := uuid.NewString()
	pending := "other.json.pending-" + token
	require.NoError(t, s.root.WriteFile(pending, []byte("not owned by this writer"), 0600))
	require.ErrorIs(t, s.write("other.json", []byte("replacement"), token), os.ErrExist)
	b, e = s.root.ReadFile(pending)
	require.NoError(t, e)
	require.Equal(t, "not owned by this writer", string(b))
	_, e = s.root.Lstat("other.json")
	require.ErrorIs(t, e, os.ErrNotExist)
}

func TestRecoveryReapsOnlyOwnerIdentifiedMetadata(t *testing.T) {
	s, _, c := fixtureStore(t)
	a, e := s.Put(context.Background(), c, strings.NewReader("preserved original"), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	dir := string(a.ID)
	raw, e := s.root.ReadFile(dir + "/owner.json")
	require.NoError(t, e)
	var o owner
	require.NoError(t, json.Unmarshal(raw, &o))
	for _, name := range []string{"owner.json", "manifest.json"} {
		// Crash after final link but before temporary unlink: both names share inode.
		require.NoError(t, s.root.Link(dir+"/"+name, dir+"/"+name+".pending-"+o.PublicationID))
	}
	unowned := dir + "/manifest.json.pending-" + uuid.NewString()
	require.NoError(t, s.root.WriteFile(unowned, []byte("unowned lookalike"), 0600))
	require.NoError(t, s.Recover(context.Background()))
	for _, name := range []string{"owner.json", "manifest.json"} {
		_, e = s.root.Lstat(dir + "/" + name + ".pending-" + o.PublicationID)
		require.ErrorIs(t, e, os.ErrNotExist)
		require.FileExists(t, filepath.Join(s.root.Name(), dir, name))
	}
	b, e := s.root.ReadFile(unowned)
	require.NoError(t, e)
	require.Equal(t, "unowned lookalike", string(b))
	b, e = s.readOriginal(a)
	require.NoError(t, e)
	require.Equal(t, "preserved original", string(b))
}

func TestRecoverySupportsPreviouslyPublishedOwner(t *testing.T) {
	s, _, c := fixtureStore(t)
	a, e := s.Put(context.Background(), c, strings.NewReader("pre-fix original"), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	// Exact original v1 shape: no publication nonce. This is a fixture conversion,
	// not production mutation or permission to overwrite published metadata.
	raw, e := json.Marshal(map[string]string{"ID": string(a.ID), "Conversation": string(c)})
	require.NoError(t, e)
	require.NoError(t, s.root.WriteFile(string(a.ID)+"/owner.json", raw, 0600))
	require.NoError(t, s.Recover(context.Background()))
	b, e := s.readOriginal(a)
	require.NoError(t, e)
	require.Equal(t, "pre-fix original", string(b))
}

func TestPublishedMetadataCorruptionStillFailsClosed(t *testing.T) {
	t.Run("marker", func(t *testing.T) {
		s, r, _ := fixtureStore(t)
		require.NoError(t, s.root.WriteFile(".webchat-attachments-v1", []byte("corrupt final"), 0600))
		other, e := NewStore(r.root, r, s.parser)
		require.ErrorIs(t, e, ErrInvalid)
		require.Nil(t, other)
		b, e := s.root.ReadFile(".webchat-attachments-v1")
		require.NoError(t, e)
		require.Equal(t, "corrupt final", string(b))
	})
	for _, kind := range []string{"manifest", "publication-identity"} {
		t.Run(kind, func(t *testing.T) {
			s, _, c := fixtureStore(t)
			a, e := s.Put(context.Background(), c, strings.NewReader("original despite corruption"), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
			require.NoError(t, e)
			dir := string(a.ID)
			if kind == "manifest" {
				require.NoError(t, s.root.WriteFile(dir+"/manifest.json", []byte("{"), 0600))
			} else {
				o := owner{ID: a.ID, Conversation: c, PublicationID: "../../original"}
				b, e := json.Marshal(o)
				require.NoError(t, e)
				require.NoError(t, s.root.WriteFile(dir+"/owner.json", b, 0600))
			}
			require.ErrorIs(t, s.Recover(context.Background()), ErrInvalid)
			b, e := s.readOriginal(a)
			require.NoError(t, e)
			require.Equal(t, "original despite corruption", string(b))
		})
	}
}
