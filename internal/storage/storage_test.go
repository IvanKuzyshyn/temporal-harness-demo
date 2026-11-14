//go:build integration

package storage_test

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/chat"
	"github.com/IvanKuzyshyn/temporal-harness-demo/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func database(t *testing.T) (*storage.Repository, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	require.NotEmpty(t, url, "run scripts/test-web-integration.sh storage; integration never silently skips")
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(cfg.ConnConfig.Database, "webchat_test_"), "refusing non-test database")
	require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
	require.Equal(t, "webchat", cfg.ConnConfig.User)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(ctx))
	return storage.New(pool), pool
}
func conversation(t *testing.T, r *storage.Repository) chat.Conversation {
	t.Helper()
	c, err := r.CreateConversation(ctx, "qwen3.5:9b")
	require.NoError(t, err)
	return c
}
func input(c chat.ConversationID) chat.CommandInput {
	return chat.CommandInput{RequestID: uuid.NewString(), ConversationID: c, Kind: chat.CommandMessage, Payload: chat.CommandPayload{Text: "Hello world"}}
}
func count(t *testing.T, p *pgxpool.Pool, table string, c chat.ConversationID) int {
	t.Helper()
	var n int
	require.NoError(t, p.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE conversation_id=$1", c).Scan(&n))
	return n
}

// Removing the transaction or request deduplication creates partial/duplicate turns.
func TestAcceptCommandAtomic(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	in := input(c.ID)
	const n = 12
	receipts := make(chan chat.CommandReceipt, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a, e := r.AcceptCommand(ctx, in); receipts <- a; errs <- e }()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var first chat.CommandReceipt
	for a := range receipts {
		if first.CommandID == "" {
			first = a
		}
		require.Equal(t, first, a)
	}
	require.NotEmpty(t, first.TurnID)
	require.Equal(t, "pending", first.DispatchState)
	require.Equal(t, "pending", first.ResultState)
	for _, table := range []string{"commands", "turns", "messages"} {
		require.Equal(t, 1, count(t, p, table, c.ID))
	}
	// Failure after turn insertion (attachment FK) rolls back every write.
	bad := input(c.ID)
	bad.Payload.AttachmentIDs = []chat.AttachmentID{chat.AttachmentID(uuid.NewString())}
	_, err := r.AcceptCommand(ctx, bad)
	require.Error(t, err)
	for _, table := range []string{"commands", "turns", "messages"} {
		require.Equal(t, 1, count(t, p, table, c.ID))
	}
	got, err := r.GetConversation(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, "Hello world", got.Title)
	cmds, err := r.ListPendingCommands(ctx, 100)
	require.NoError(t, err)
	found := false
	for _, cmd := range cmds {
		if cmd.Receipt.CommandID == first.CommandID {
			found = true
			require.Equal(t, in.Payload, cmd.Input.Payload)
		}
	}
	require.True(t, found)
	require.NoError(t, r.RecordCommandResult(ctx, c.ID, first.CommandID, "dispatched", "accepted", []byte(`{"ok":true,"count":1e3}`)))
	again, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	require.Equal(t, first.CommandID, again.CommandID)
	require.Equal(t, "accepted", again.ResultState)
	require.NoError(t, r.RecordCommandResult(ctx, c.ID, first.CommandID, "dispatched", "accepted", []byte(`{ "ok" : true, "count": 1e3 }`)))
	require.ErrorIs(t, r.RecordCommandResult(ctx, c.ID, first.CommandID, "dispatched", "rejected", []byte(`{}`)), storage.ErrConflict)
}

// Trusting a supplied hash or omitting kind from the canonical digest permits ID reuse.
func TestCommandPayloadConflict(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	in := input(c.ID)
	a, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	in.Payload.Text = "changed"
	in.PayloadDigest = a.PayloadDigest
	_, err = r.AcceptCommand(ctx, in)
	require.ErrorIs(t, err, storage.ErrConflict)
	in.PayloadDigest = ""
	_, err = r.AcceptCommand(ctx, in)
	require.ErrorIs(t, err, storage.ErrConflict)
	in.Kind = chat.CommandRevoke
	in.Payload = chat.CommandPayload{}
	_, err = r.AcceptCommand(ctx, in)
	require.ErrorIs(t, err, storage.ErrConflict)
	in.ConversationID = conversation(t, r).ID
	_, err = r.AcceptCommand(ctx, in)
	require.NoError(t, err, "request IDs are scoped to conversation")
	_, err = r.CreateConversation(ctx, "invented-model")
	require.ErrorIs(t, err, chat.ErrInvalid)
}

func assistant(c chat.ConversationID, turn chat.TurnID) chat.MessageRecord {
	return chat.MessageRecord{EntryID: uuid.NewString(), ConversationID: c, TurnID: turn, Role: "assistant", Text: "answer", LogicalStep: 1, ResultKind: "response", CompletionState: "completed"}
}

// Repeated activity completions must not create a second logical answer; attempts remain separate.
func TestLogicalResultIdempotent(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	a, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	m := assistant(c.ID, a.TurnID)
	for attempt := 1; attempt <= 2; attempt++ {
		require.NoError(t, r.RecordModelAttempt(ctx, chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: a.TurnID, LogicalStep: 1, Attempt: attempt, Status: "failed", Error: "interrupted"}))
	}
	const n = 10
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- r.AppendMessage(ctx, m) }()
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}
	require.Equal(t, 2, count(t, p, "messages", c.ID))
	require.Equal(t, 2, count(t, p, "model_steps", c.ID))
	changed := m
	changed.Text = "other"
	require.ErrorIs(t, r.AppendMessage(ctx, changed), storage.ErrConflict)
	changed = m
	changed.EntryID = uuid.NewString()
	require.ErrorIs(t, r.AppendMessage(ctx, changed), storage.ErrConflict, "logical completion identity cannot change")
	_, err = p.Exec(ctx, "UPDATE messages SET text='mutated' WHERE entry_id=$1", m.EntryID)
	require.Error(t, err, "immutable message enforced by DB")
	other := conversation(t, r)
	changed = m
	changed.EntryID = uuid.NewString()
	changed.ConversationID = other.ID
	require.Error(t, r.AppendMessage(ctx, changed), "turn ownership must be DB-enforced")
}

// Timestamp cursors and pre-commit sequence allocation lose late writers or duplicate equal timestamps.
func TestHistoryStablePagination(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	for i := 0; i < 7; i++ {
		_, err := r.AcceptCommand(ctx, input(c.ID))
		require.NoError(t, err)
	}
	first, err := r.ListMessages(ctx, c.ID, "", 3)
	require.NoError(t, err)
	require.Len(t, first.Messages, 3)
	require.NotEmpty(t, first.Before)
	// A transaction holding the conversation row must serialize later appends until commit.
	tx, err := p.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "SELECT id FROM conversations WHERE id=$1 FOR UPDATE", c.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := r.AcceptCommand(ctx, input(c.ID)); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("append bypassed conversation lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-done)
	seen := map[string]bool{}
	last := int64(1 << 62)
	page := first
	for {
		for i := len(page.Messages) - 1; i >= 0; i-- {
			m := page.Messages[i]
			require.Less(t, m.Sequence, last)
			last = m.Sequence
			require.False(t, seen[m.EntryID])
			seen[m.EntryID] = true
		}
		if page.Before == "" {
			break
		}
		page, err = r.ListMessages(ctx, c.ID, page.Before, 3)
		require.NoError(t, err)
	}
	require.Len(t, seen, 7, "append after first page must not shift its snapshot")
	latest, err := r.ListMessages(ctx, c.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, latest.Messages, 8)
	other := conversation(t, r)
	_, err = r.ListMessages(ctx, other.ID, first.Before, 3)
	require.ErrorIs(t, err, chat.ErrInvalid)
	_, err = r.ListMessages(ctx, c.ID, "garbage", 3)
	require.ErrorIs(t, err, chat.ErrInvalid)
}

func attachment(c chat.ConversationID) chat.Attachment {
	return chat.Attachment{ID: chat.AttachmentID(uuid.NewString()), ConversationID: c, OriginalName: "sample.txt", MediaType: "text/plain", Size: 4, Checksum: strings.Repeat("a", 64), StorageKey: "opaque/" + uuid.NewString(), Status: "staged"}
}

// Omitting ownership in either FK or lookup exposes another conversation's file.
func TestAttachmentForeignConversationRejected(t *testing.T) {
	r, p := database(t)
	a, b := conversation(t, r), conversation(t, r)
	f := attachment(a.ID)
	require.NoError(t, r.CreateAttachment(ctx, f))
	require.NoError(t, r.CreateAttachment(ctx, f))
	_, err := r.GetAttachment(ctx, b.ID, f.ID)
	require.ErrorIs(t, err, storage.ErrNotFound)
	in := input(b.ID)
	in.Payload.AttachmentIDs = []chat.AttachmentID{f.ID}
	_, err = r.AcceptCommand(ctx, in)
	require.Error(t, err)
	require.Equal(t, 0, count(t, p, "turns", b.ID))
	in.ConversationID = a.ID
	_, err = r.AcceptCommand(ctx, in)
	require.Error(t, err, "staged file is not durable/ready")
	require.NoError(t, r.MarkAttachmentReady(ctx, a.ID, f.ID, f.Checksum))
	_, err = r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	f.OriginalName = "changed"
	require.ErrorIs(t, r.CreateAttachment(ctx, f), storage.ErrConflict)
	// A new join row must not mutate an already committed immutable message.
	another := attachment(a.ID)
	require.NoError(t, r.CreateAttachment(ctx, another))
	require.NoError(t, r.MarkAttachmentReady(ctx, a.ID, another.ID, another.Checksum))
	page, err := r.ListMessages(ctx, a.ID, "", 10)
	require.NoError(t, err)
	_, err = p.Exec(ctx, "INSERT INTO message_attachments(conversation_id,entry_id,attachment_id,ordinal) VALUES($1,$2,$3,1)", a.ID, page.Messages[0].EntryID, another.ID)
	require.Error(t, err, "cannot attach a file after the message commit")
}

func TestControlOwnershipAndIntent(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	other := conversation(t, r)
	a, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	for _, kind := range []chat.CommandKind{chat.CommandStop, chat.CommandReplace} {
		in := input(other.ID)
		in.Kind = kind
		in.Payload = chat.CommandPayload{ExpectedActiveTurnID: a.TurnID}
		if kind == chat.CommandReplace {
			in.Payload.Text = "replacement"
		}
		_, err = r.AcceptCommand(ctx, in)
		require.Error(t, err)
		in.ConversationID = c.ID
		receipt, err := r.AcceptCommand(ctx, in)
		require.NoError(t, err)
		require.Equal(t, "pending", receipt.ResultState, "storage is not execution authority")
	}
}

func TestMigrationCycle(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PHASE") != "migration" {
		return
	}
	_, p := database(t)
	url := os.Getenv("WEBCHAT_TEST_DATABASE_URL")
	// Both targets live in the runner's unique disposable container. Use the
	// existing Temporal role credentials, not changed/granted app permissions.
	forbiddenURL := os.Getenv("WEBCHAT_TEST_TEMPORAL_DATABASE_URL")
	require.NotEmpty(t, forbiddenURL)
	forbiddenConfig, err := pgxpool.ParseConfig(forbiddenURL)
	require.NoError(t, err)
	require.Equal(t, p.Config().ConnConfig.Host, forbiddenConfig.ConnConfig.Host)
	require.Equal(t, p.Config().ConnConfig.Port, forbiddenConfig.ConnConfig.Port)
	require.Equal(t, "temporal", forbiddenConfig.ConnConfig.User)
	require.Equal(t, "temporal", forbiddenConfig.ConnConfig.Database)
	forbidden, err := pgxpool.NewWithConfig(ctx, forbiddenConfig)
	require.NoError(t, err)
	t.Cleanup(forbidden.Close)
	assertNoMetadata := func() {
		t.Helper()
		var name *string
		require.NoError(t, forbidden.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations')::text").Scan(&name))
		require.Nil(t, name, "rejected target must never receive migration metadata")
	}
	assertNoMetadata()
	for _, alias := range []string{"dbname", "database"} {
		attack := url + "&user=temporal&password=" + neturl.QueryEscape(forbiddenConfig.ConnConfig.Password) + "&" + alias + "=temporal"
		if err := storage.Migrate(attack, "up"); err == nil {
			t.Error("effective Temporal target was accepted")
		}
		assertNoMetadata()
	}
	// Input-order alias resolution selects the application DB. Re-encoding the
	// query through url.Values before connecting would reverse these aliases.
	allowedAliases := url + "&dbname=temporal&database=" + neturl.QueryEscape(p.Config().ConnConfig.Database)
	require.NoError(t, storage.Migrate(allowedAliases, "up"))
	assertNoMetadata()
	require.NoError(t, storage.Migrate(url, "down"))
	var name *string
	require.NoError(t, p.QueryRow(ctx, "SELECT to_regclass('public.conversations')::text").Scan(&name))
	require.Nil(t, name)
	require.NoError(t, storage.Migrate(url, "up"))
	require.NoError(t, storage.Migrate(url, "up"))
	for _, table := range []string{"conversations", "turns", "messages", "model_steps", "tool_calls", "approvals", "attachments", "conversation_summaries", "commands", "events"} {
		require.NoError(t, p.QueryRow(ctx, "SELECT to_regclass($1)::text", table).Scan(&name))
		require.NotNil(t, name, table)
	}
}

func TestDatabaseIsolation(t *testing.T) {
	_, p := database(t)
	for _, db := range []string{"temporal", "temporal_visibility"} {
		cfg := p.Config()
		cfg.ConnConfig.Database = db
		forbidden, err := pgxpool.NewWithConfig(ctx, cfg)
		require.NoError(t, err)
		err = forbidden.Ping(ctx)
		forbidden.Close()
		require.Error(t, err, "application role must not connect to "+db)
	}
	_, err := p.Exec(ctx, "CREATE DATABASE storage_escape")
	require.Error(t, err)
}

// The runner invokes these on opposite sides of docker compose restart, not a pool reconnect.
func TestRestartWrite(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PHASE") != "restart-write" {
		return
	}
	r, _ := database(t)
	c := conversation(t, r)
	in := input(c.ID)
	a, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("WEBCHAT_TEST_RECEIPT"), []byte(fmt.Sprintf("%s\n%s\n%s", c.ID, in.RequestID, a.CommandID)), 0600))
}
func TestRestartRead(t *testing.T) {
	if os.Getenv("WEBCHAT_TEST_PHASE") != "restart-read" {
		return
	}
	r, _ := database(t)
	data, err := os.ReadFile(os.Getenv("WEBCHAT_TEST_RECEIPT"))
	require.NoError(t, err)
	parts := strings.Split(string(data), "\n")
	require.Len(t, parts, 3)
	in := input(chat.ConversationID(parts[0]))
	in.RequestID = parts[1]
	a, err := r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	require.Equal(t, parts[2], string(a.CommandID))
	page, err := r.ListMessages(ctx, in.ConversationID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	require.Equal(t, "Hello world", page.Messages[0].Text)
}

// Equal and backdated timestamps must not reorder a late commit behind a cursor.
func TestHistoryEqualTimestampsAndLateCommit(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	a, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	tx, err := p.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	insert := `INSERT INTO messages(entry_id,conversation_id,turn_id,role,text,logical_step,result_kind,completion_state,payload_digest,created_at) VALUES($1,$2,$3,'assistant','answer',$4,'response','completed',$5,'2000-01-01T00:00:00Z')`
	lateID, nextID := uuid.NewString(), uuid.NewString()
	_, err = tx.Exec(ctx, insert, lateID, c.ID, a.TurnID, 1, strings.Repeat("a", 64))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := p.Exec(ctx, insert, nextID, c.ID, a.TurnID, 2, strings.Repeat("b", 64)); done <- err }()
	visible, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	require.Len(t, visible.Messages, 1)
	select {
	case err := <-done:
		t.Fatalf("late writer bypassed sequence lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-done)
	latest, err := r.ListMessages(ctx, c.ID, "", 1)
	require.NoError(t, err)
	require.Equal(t, nextID, latest.Messages[0].EntryID)
	older, err := r.ListMessages(ctx, c.ID, latest.Before, 1)
	require.NoError(t, err)
	require.Equal(t, lateID, older.Messages[0].EntryID)
	require.Equal(t, latest.Messages[0].CreatedAt, older.Messages[0].CreatedAt)
	oldest, err := r.ListMessages(ctx, c.ID, older.Before, 1)
	require.NoError(t, err)
	require.Equal(t, "user", oldest.Messages[0].Role)
}

func TestSchemaResultTurnOwnership(t *testing.T) {
	r, _ := database(t)
	c := conversation(t, r)
	a, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	b, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	m := assistant(c.ID, a.TurnID)
	require.NoError(t, r.AppendMessage(ctx, m))
	attempt := chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: b.TurnID, LogicalStep: 1, Attempt: 1, Status: "completed", ResultEntryID: m.EntryID}
	require.Error(t, r.RecordModelAttempt(ctx, attempt), "attempt cannot claim another turn's result even within one conversation")
}

// Projection identities and foreign keys must survive retries without crossing scope.
func TestProjectionRecords(t *testing.T) {
	r, p := database(t)
	c := conversation(t, r)
	a, err := r.AcceptCommand(ctx, input(c.ID))
	require.NoError(t, err)
	tool := chat.ToolCall{ID: uuid.NewString(), ConversationID: c.ID, TurnID: a.TurnID, LogicalKey: "step-1/tool-1", Name: "web_fetch", Arguments: []byte(`{"url":"https://example.com"}`), ActionHash: strings.Repeat("b", 64), ParentWorkflowID: c.WorkflowID, Status: "pending"}
	require.NoError(t, r.RecordToolCall(ctx, tool))
	require.NoError(t, r.RecordToolCall(ctx, tool))
	tool.Arguments = []byte(`{"url":"https://other.example"}`)
	require.ErrorIs(t, r.RecordToolCall(ctx, tool), storage.ErrConflict)
	require.NoError(t, r.CompleteToolCall(ctx, c.ID, tool.ID, "completed", []byte(`{"text":"page","count":1e3}`), []byte(`["https://example.com"]`)))
	require.NoError(t, r.CompleteToolCall(ctx, c.ID, tool.ID, "completed", []byte(`{ "text" : "page", "count":1e3 }`), []byte(`["https://example.com"]`)))
	require.ErrorIs(t, r.CompleteToolCall(ctx, c.ID, tool.ID, "failed", nil, nil), storage.ErrConflict)
	approval := chat.Approval{ID: uuid.NewString(), ConversationID: c.ID, ToolCallID: tool.ID, ActionHash: tool.ActionHash, Deadline: time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)}
	require.NoError(t, r.RecordApproval(ctx, approval))
	require.NoError(t, r.RecordApproval(ctx, approval))
	decidedAt := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, r.RecordApprovalDecision(ctx, c.ID, approval.ID, chat.ApprovalOnce, 1, decidedAt))
	require.NoError(t, r.RecordApprovalDecision(ctx, c.ID, approval.ID, chat.ApprovalOnce, 1, decidedAt))
	require.ErrorIs(t, r.RecordApprovalDecision(ctx, c.ID, approval.ID, chat.ApprovalDeny, 1, decidedAt), storage.ErrConflict)
	in := input(c.ID)
	in.Kind = chat.CommandDecide
	in.Payload = chat.CommandPayload{ApprovalID: approval.ID, ActionHash: approval.ActionHash, Decision: chat.ApprovalOnce}
	_, err = r.AcceptCommand(ctx, in)
	require.NoError(t, err)
	in.RequestID = uuid.NewString()
	in.Payload.ActionHash = strings.Repeat("c", 64)
	_, err = r.AcceptCommand(ctx, in)
	require.Error(t, err)
	other := conversation(t, r)
	approval.ID = uuid.NewString()
	approval.ConversationID = other.ID
	require.Error(t, r.RecordApproval(ctx, approval))
	page, err := r.ListMessages(ctx, c.ID, "", 20)
	require.NoError(t, err)
	summary := chat.Summary{ID: uuid.NewString(), ConversationID: c.ID, Version: 1, CoveredSequence: page.Messages[0].Sequence, Text: "summary", Model: c.Model, Budget: []byte(`{"tokens":10}`)}
	require.NoError(t, r.SaveSummary(ctx, summary))
	require.NoError(t, r.SaveSummary(ctx, summary))
	got, err := r.LatestSummary(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, summary.Text, got.Text)
	summary.Text = "changed"
	require.ErrorIs(t, r.SaveSummary(ctx, summary), storage.ErrConflict)
	summary.ID = uuid.NewString()
	summary.ConversationID = other.ID
	require.Error(t, r.SaveSummary(ctx, summary))
	attempt := chat.ModelAttempt{ID: uuid.NewString(), ConversationID: c.ID, TurnID: a.TurnID, LogicalStep: 1, Attempt: 1, Status: "running", Usage: []byte(`{"tokens":3}`)}
	require.NoError(t, r.RecordModelAttempt(ctx, attempt))
	require.NoError(t, r.RecordModelAttempt(ctx, attempt))
	attempt.Error = "changed"
	require.ErrorIs(t, r.RecordModelAttempt(ctx, attempt), storage.ErrConflict)
	event := chat.Event{ID: uuid.NewString(), ConversationID: c.ID, TurnID: a.TurnID, LogicalStep: 1, Attempt: 1, Type: "attempt.failed", Payload: []byte(`{"reason":"interrupted"}`)}
	require.NoError(t, r.AppendEvent(ctx, event))
	require.NoError(t, r.AppendEvent(ctx, event))
	require.Equal(t, 1, count(t, p, "events", c.ID))
	require.NoError(t, r.CompleteModelAttempt(ctx, c.ID, attempt.ID, "failed", []byte(`{"tokens":1e3}`), "", "interrupted"))
	require.NoError(t, r.CompleteModelAttempt(ctx, c.ID, attempt.ID, "failed", []byte(`{"tokens":1e3}`), "", "interrupted"))
	require.ErrorIs(t, r.CompleteModelAttempt(ctx, c.ID, attempt.ID, "completed", nil, "", ""), storage.ErrConflict)
	events, err := r.ListEvents(ctx, c.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(1), events[0].Sequence)
	events, err = r.ListEvents(ctx, c.ID, 1, 20)
	require.NoError(t, err)
	require.Empty(t, events)
	event.Payload = []byte(`{"reason":"changed"}`)
	require.ErrorIs(t, r.AppendEvent(ctx, event), storage.ErrConflict)
}
