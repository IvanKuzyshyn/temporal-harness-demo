//go:build integration

package attachments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/tools"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func attachmentDB(t *testing.T) (*storage.Repository, *pgxpool.Pool) {
	t.Helper()
	cfg, e := pgxpool.ParseConfig(os.Getenv("WEBCHAT_TEST_DATABASE_URL"))
	require.NoError(t, e)
	require.True(t, strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_"))
	require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
	p, e := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, e)
	t.Cleanup(p.Close)
	return storage.New(p), p
}

type dyingRepository struct{ *storage.Repository }

func (r dyingRepository) CreateAttachment(context.Context, chat.Attachment) error {
	os.Exit(73)
	return nil
}
func TestAttachmentFinalizeProcessDeath(t *testing.T) {
	if os.Getenv("WEBCHAT_ATTACHMENT_DIE") == "1" {
		r, _ := attachmentDB(t)
		s, e := NewStore(os.Getenv("WEBCHAT_ATTACHMENT_ROOT"), dyingRepository{r}, nil)
		require.NoError(t, e)
		_, e = s.Put(context.Background(), chat.ConversationID(os.Getenv("WEBCHAT_ATTACHMENT_CONVERSATION")), strings.NewReader("original survives process death"), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
		t.Fatal(e)
		return
	}
	r, p := attachmentDB(t)
	ctx := context.Background()
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	root := t.TempDir()
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(deadline, os.Args[0], "-test.run=^TestAttachmentFinalizeProcessDeath$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "WEBCHAT_ATTACHMENT_DIE=1", "WEBCHAT_ATTACHMENT_ROOT="+root, "WEBCHAT_ATTACHMENT_CONVERSATION="+string(c.ID))
	e = cmd.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, e, &exit)
	require.Equal(t, 73, exit.ExitCode())
	var n int
	require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM attachments WHERE conversation_id=$1", c.ID).Scan(&n))
	require.Zero(t, n)
	s, e := NewStore(root, r, nil)
	require.NoError(t, e)
	defer s.Close()
	require.NoError(t, s.Recover(ctx))
	require.NoError(t, s.Recover(ctx))
	var id chat.AttachmentID
	require.NoError(t, p.QueryRow(ctx, "SELECT id::text FROM attachments WHERE conversation_id=$1 AND status='ready'", c.ID).Scan(&id))
	f, e := s.Open(ctx, c.ID, id)
	require.NoError(t, e)
	b, e := io.ReadAll(f)
	f.Close()
	require.NoError(t, e)
	require.Equal(t, "original survives process death", string(b))
	t.Log("child exited73 at metadata commit boundary; fresh store recovered finalized original and SQL ready exactly once")
}

type extractionFaultRepository struct {
	*storage.Repository
	before, after bool
	writes        int
}

func (r *extractionFaultRepository) SaveAttachmentExtraction(ctx context.Context, s chat.ApprovalScope, id chat.AttachmentID, d chat.ExtractedDocument) error {
	r.writes++
	if r.before {
		return errors.New("injected extraction write unavailable")
	}
	e := r.Repository.SaveAttachmentExtraction(ctx, s, id, d)
	if e == nil && r.after {
		return errors.New("injected extraction lost acknowledgment")
	}
	return e
}
func TestAttachmentExtractionAuthorizedImmutable(t *testing.T) {
	r, p := attachmentDB(t)
	ctx := context.Background()
	c, e := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, e)
	fault := &extractionFaultRepository{Repository: r}
	s, e := NewStore(t.TempDir(), fault, nil)
	require.NoError(t, e)
	defer s.Close()
	a, e := s.Put(ctx, c.ID, strings.NewReader(strings.Repeat("é", 200001)), UploadMetadata{Name: "a.txt", MediaType: "text/plain"})
	require.NoError(t, e)
	rc, e := r.AcceptCommand(ctx, chat.CommandInput{ConversationID: c.ID, RequestID: uuid.NewString(), Kind: chat.CommandMessage, Payload: chat.CommandPayload{AttachmentIDs: []chat.AttachmentID{a.ID}}})
	require.NoError(t, e)
	ref := chat.TurnRef{ConversationID: c.ID, TurnID: rc.TurnID}
	require.NoError(t, r.StartChatTurn(ctx, ref))
	args, _ := json.Marshal(map[string]string{"attachmentId": string(a.ID), "instruction": "extract"})
	call, e := tools.NewWebRegistry(nil).Canonicalize("file_upload", args, a.Checksum)
	require.NoError(t, e)
	call.ID = uuid.NewString()
	call.ActionHash = strings.Repeat("b", 64)
	require.NoError(t, r.SaveChatModelResult(ctx, chat.ChatModelResult{TurnRef: ref, Calls: []chat.WebModelCall{call}}))
	actions, e := r.PrepareWebActions(ctx, ref)
	require.NoError(t, e)
	action := actions.Actions[0]
	_, e = s.Extract(ctx, action.Scope, a.ID)
	require.Error(t, e, "pending must not read originals")
	action.Outcome = "authorized"
	require.NoError(t, r.RecordWebAuthorization(ctx, action))
	_, _, e = r.BeginWebTool(ctx, action.Scope, uuid.NewString(), 1)
	require.NoError(t, e)
	fault.before = true
	_, e = s.Extract(ctx, action.Scope, a.ID)
	require.ErrorIs(t, e, ErrPersistence)
	_, e = r.ReadAttachmentExtraction(ctx, c.ID, a.ID)
	require.ErrorIs(t, e, storage.ErrNotFound)
	fault.before = false
	fault.after = true
	_, e = s.Extract(ctx, action.Scope, a.ID)
	require.ErrorIs(t, e, ErrPersistence)
	doc, e := s.Extract(ctx, action.Scope, a.ID)
	require.Equal(t, 2, fault.writes, "cached retry must not rewrite or re-extract")
	require.NoError(t, e)
	require.Equal(t, "completed", doc.Status)
	require.Len(t, []rune(doc.Text), 200000)
	require.True(t, doc.Truncated)
	again, e := s.Extract(ctx, action.Scope, a.ID)
	require.NoError(t, e)
	require.Equal(t, doc, again)
	changedDoc := doc
	changedDoc.Text = "rewritten"
	changedDoc.Sections = nil
	require.ErrorIs(t, r.SaveAttachmentExtraction(ctx, action.Scope, a.ID, changedDoc), storage.ErrConflict)
	var text string
	require.NoError(t, p.QueryRow(ctx, "SELECT extracted_text FROM attachments WHERE id=$1", a.ID).Scan(&text))
	require.Equal(t, doc.Text, text)
	changed := action.Scope
	changed.ConversationID = chat.ConversationID(uuid.NewString())
	_, e = s.Extract(ctx, changed, a.ID)
	require.Error(t, e)
	changed = action.Scope
	changed.ContentRef = strings.Repeat("c", 64)
	_, e = s.Extract(ctx, changed, a.ID)
	require.Error(t, e)
}
