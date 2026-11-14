# Web-chat application storage (Tasks 2–6)

Schema **5** adds step-aware immutable model candidates, recorded approval scope
and outcomes, effective versioned policy, exact tool authorization/result ownership
and a per-body tool cleanup ledger. See [recorded approvals](web-chat-approvals.md)
for the current interfaces, migration constraints and Task7/8/9 handoff. Earlier
text-only/cleanup contracts below remain the baseline except as extended there.

This package persists application content and **intent**, not execution authority.
Only Temporal can adjudicate active-turn freshness, replacement races, tool
consent, policy revision, cancellation, and completion. Never interpret a
successful `AcceptCommand` or a projection row as authorization.

Task 3 adds idempotent creation, stable sidebar snapshots, projection reads and
fenced command claims in migration `000002`. See [the private API contract](web-api.md)
for those exported interfaces, dispatch ordering/backoff, and runtime configuration.
Production dispatch uses `ClaimCommands`/`SettleClaim`, not the older
unleased `ListPendingCommands` read helper.

Task4 adds migration `000003`, direct `GetTurn`/bounded `RecentChatExchanges`,
`CheckChatCommand`/`RecordChatAdmission`, `StartChatTurn`,
`BeginChatModelAttempt`, `GetChatModelResult`/`SaveChatModelResult`, and atomic
`PersistTurnResult`. See [web-chat-temporal.md](web-chat-temporal.md) for exact
receipt ordering, bounded carry and completion authority. Task5 fix1 requires schema4 (the earlier Task5 slice used schema3):
`MarkChatCancelling` projects the workflow-owned target; accepted control receipts
also project cancelling without resurrecting a terminal turn. Cancelled
`PersistTurnResult` atomically closes running attempts, writes `turn.cancelled`,
and retains any cached provider candidate as an immutable **incomplete** cancelled
assistant message, never a final answer. Rejected replacement intents become
failed with `stale_target`/`control_conflict`/`invalid_command`. `GetTurn` now returns
the target's original prompt and ordered attachment IDs as provenance, never
attachment bytes/extracted text. These content fields stay in activities, not
workflow carry. Domain types are in
`internal/chat/execution.go`; the source interfaces remain authoritative.

Admission recovery uses bounded `CommandIdentity`/`AdmissionCheckpoint` values,
not command payloads. `ResumeChatAdmission(ctx, checkpoint)` rechecks all stored
identities and the original digest, then either reads the receipt (`check`) or
persists the workflow's already replayable decision (`receipt`). It never decides
whether to enqueue. The original full-command activity path still recomputes the
payload digest before using the shared identity helpers.

`ResolveClosedChatCommand(ctx, command, enqueued, observedDecision...)` is terminal reconciliation,
called only after confirming closure and querying that exact run's replayed
active turn/inbox. A missing receipt alone proves nothing. Under the conversation
lock, existing receipts win; enqueue evidence or durable execution state implies
accepted admission, otherwise the command is rejected `conversation_closed`.
Task5's optional observed decision comes from the pinned run's replayed control
claim or in-flight/checkpoint receipt and preserves exact control outcomes/IDs.
The resolved receipt is persisted before returning, fencing late conflicting
admission writers. Accepted here is not a completion claim or a session restart.
The method changes neither turn projections nor Task3 dispatcher claim fencing.

## Entry points

Use `storage.New(pool *pgxpool.Pool) *storage.Repository`; the caller owns the
pool. Use the `webchat` database role. `storage.Migrate(databaseURL, "up")`
applies embedded application migrations with pinned golang-migrate/pgx.
`"down"` is explicitly destructive and is used only on an empty, test-owned
application database by the integration runner. Never point migrations at a
Temporal database; the migration helper rejects both Temporal database names
and non-application roles, and the application role cannot connect to those DBs.
It parses the pgx connection configuration once, validates the **effective** user
and database after URL query overrides, aliases, environment, and service-file
settings, then connects from that same configuration without URI re-encoding or
reparsing. Validation happens before constructing the migration driver (which
can create `schema_migrations`). An empty effective database is rejected rather
than left to server defaults. Startup `role`, `session_authorization`, and
`options` (including `PGOPTIONS`/service-file options) are rejected; this helper
also uses fixed migration settings, not custom `x-` driver parameters. Ordinary
application URLs with SSL mode, connect timeout, and application name remain
supported. Duplicate/aliased parameters use pgx's input-order resolution; only
the final resolved application identity is used.

All domain structs are in `internal/chat/contracts.go`. Identity strings must
be nonzero, lowercase, hyphenated UUIDs. Models are exactly `qwen3.5:9b` and
`gemma4:e4b`; the API may default to Qwen but storage never silently substitutes
an unsupported model.

### Conversation, commands, history

- `CreateConversation(ctx, model) (chat.Conversation, error)` creates a UUID and
  stable workflow ID `web-chat/<uuid>`; first accepted prompt sets the title
  (whitespace collapsed, first 80 Unicode code points, `Attachment` if no text).
- `GetConversation(ctx, id) (chat.Conversation, error)` reads metadata.
- `AcceptCommand(ctx, chat.CommandInput) (chat.CommandReceipt, error)` accepts
  `message`, `replace`, `stop`, `decide`, or `revoke`. Message/replacement creates
  the queued turn, immutable user message, attachment links, and outbox entry in
  **one transaction**. A missing, foreign, or staged attachment rolls it all back.
- `ListPendingCommands(ctx, limit) ([]chat.StoredCommand, error)` includes both
  undispatched intents and dispatched commands awaiting an authoritative result.
  Multiple dispatchers may read the same row: submit the persisted **CommandID
  as the Temporal Update ID**. No cross-database exactly-once claim or leasing
  is made here. Consumers must process the bounded batch, not stop at its first
  pending item.
- `RecordCommandResult(ctx, conversationID, commandID, dispatchState, resultState,
  body []byte) error` monotonically records `pending|dispatched` and
  `pending|accepted|rejected`. A terminal receipt is immutable; identical retries
  succeed. This result describes workflow acceptance/rejection, not a completed
  assistant response. No turn execution projection changes automatically.
- `AppendMessage(ctx, chat.MessageRecord) error` writes an immutable logical
  result. Retain its EntryID across activity retries; uniqueness is additionally
  enforced on `(turn_id, logical_step, result_kind)`. Changed payload or a new
  EntryID for the same logical result conflicts. A partial draft must use a
  distinct result kind/entry from the final result; preferably use attempt events.
- `ListMessages(ctx, conversationID, before string, limit int)
  (chat.MessagePage, error)` returns each page oldest-to-newest. Empty `before`
  means the newest page; returned `Before` fetches the next older page. Limits
  are 1–200. Cursors are opaque, versioned, and conversation-bound.

Request deduplication is scoped to **(ConversationID, RequestID), across command
kinds**, with no TTL or FIFO eviction. Different conversations are distinct
request domains. `CommandInput.Digest()` validates the typed payload and hashes
Go's deterministic JSON encoding of `{kind,payload}`. Optional `PayloadDigest`
is a caller assertion, never trusted; the server recomputes it. Empty/nil
attachment lists normalize; array order, text, expected target, action hash, and
decision remain significant. Unknown fields must be rejected by future HTTP
payload decoding, rather than discarded before validation. A duplicate returns
the original command/turn identity with its **current durable receipt state**.

### Attachment metadata

- `CreateAttachment(ctx, chat.Attachment) error`: idempotent staged metadata
  only, no extracted content or derived images on this path.
- `MarkAttachmentReady(ctx, conversationID, attachmentID, checksum) error`:
  caller attests that bytes are durably finalized under the opaque storage key;
  it must do the filesystem write/checksum itself. This does **not** approve
  access to the content.
- `GetAttachment(ctx, conversationID, attachmentID) (chat.Attachment, error)`:
  ownership-scoped lookup. Preapproval processing is absent from this task.

Original file/image bytes never enter Postgres. Schema includes extraction
status/text/derived-image metadata for the later authorized processing task.

### Worker projection records

Schema4 adds `turns.model_invocations_closed` and `chat_model_invocations`
(id, conversation/turn, Temporal attempt, registered_at, cleanup_confirmed_at).
Each activity body gets its own UUID, even a duplicate delivery of one attempt.
`RegisterChatModelInvocation(ctx,ref,id,attempt)` must commit before provider work;
it rejects closed admission and never reauthorizes an existing invocation ID.
`AcknowledgeChatModelCleanup(ctx,ref,id)` is owner-bound/idempotent and runs only
after all body work and goroutines join. `SealChatModelInvocations(ctx,ref)` closes
admission permanently and returns the count of missing acknowledgments. These
transactions share the conversation lock. A racing registration is therefore
visible or rejected, and sealed zero is stable. Neither `model_steps` failure nor
a saved canonical candidate is acknowledgment. `PersistTurnResult` independently
rejects settlement with `ErrChatCleanupUnconfirmed` while a row remains unacknowledged
if the requested outcome is `cancelled` **or** the target is already `cancelling`.
Thus a caller cannot bypass a controlled target's barrier by requesting completed/
failed. Ordinary completed/failed outcomes may settle despite missing cleanup
acknowledgments, retaining Task4's hard-worker-loss recovery and canonical fencing.
All successful settlements seal admission atomically. Rows with missing
acknowledgments are not acknowledged or deleted by settlement; ordinary logical
completion is not cleanup proof. There is no schema change for this distinction.

Apply migration4 with old API/workers stopped; do not hot-mix old workers that
lack registration with the new protocol. Upgrade backfills existing nonterminal
model attempts as **unacknowledged**, even
if their publisher projection says failed/interrupted. It does not fabricate
cleanup evidence. Pre-upgrade active work may require operator investigation;
there is no old workflow-history compatibility/reset promise. Down migration
removes only the new ledger/column; downgrade must not run alongside active workers.


- `BeginChatModelAttempt(ctx, chat.ModelAttempt) (cached bool, err error)` is the
  text-chat registration path. It atomically checks the canonical candidate,
  registers the new running attempt and fences older attempts under the same
  conversation lock used for candidate publication. A cached winner skips new
  registration/inference; its owning attempt remains available for settlement.
  `SaveChatModelResult` rejects stale attempt publication with
  `ErrStaleChatAttempt`; immutable changed-payload conflicts remain `ErrConflict`.
  `PersistTurnResult` verifies that exactly one winning running attempt was
  completed/linked, rolling back all final output on a mismatched projection.
- `RecordModelAttempt(ctx, chat.ModelAttempt) error` registers a running attempt
  or an already-settled snapshot; `(turn, logical step, attempt)` is unique.
- `CompleteModelAttempt(ctx, conversationID, id, status string, usage []byte,
  resultEntryID, reason string) error` settles a running attempt exactly once.
  A result reference must belong to that conversation **and turn**.
- `RecordToolCall(ctx, chat.ToolCall) error` registers the immutable action and
  optional settled snapshot; `(turn, logical key)` is unique.
- `CompleteToolCall(ctx, conversationID, id, status string, result, sources []byte)
  error` settles pending/running calls exactly once. Terminal outcomes are
  `completed|failed|denied|cancelled`.
- `RecordApproval(ctx, chat.Approval) error` registers a workflow projection,
  binding conversation, tool ID, action hash, deadline, and revision.
- `RecordApprovalDecision(ctx, conversationID, id string, decision
  chat.ApprovalDecision, revision int64, at time.Time) error` records a previously
  adjudicated decision exactly once. It never grants consent or changes policy.
- `SaveSummary(ctx, chat.Summary) error` writes immutable versions with a valid
  same-conversation covered-message sequence and the conversation's exact model.
- `LatestSummary(ctx, conversationID) (chat.Summary, error)` reads the latest.
- `AppendEvent(ctx, chat.Event) error` writes a stable-ID event; `(logical step,
  attempt)` is optional but, when present, references a registered model attempt.
- `ListEvents(ctx, conversationID, after int64, limit int) ([]chat.Event, error)`
  replays ascending sequence, exclusively after the supplied sequence.

Creation digests remain unchanged when projections settle; late retries of the
original creation remain harmless. Repeating a terminal projection with changed
content conflicts. JSONB projection results are compared using database JSONB
equality, including equivalent numeric notation. Live/attempt events are not
completed logical responses. Events have a 64KiB per-row cap; event retention,
semantic payload filtering (no image bytes), SSE buffering, and expiry/snapshot
policy belong to the transport task. Task4's `PersistTurnResult` now atomically
writes final message/turn-status/event and fences changed terminal outcomes;
standalone `AppendMessage`/`AppendEvent` calls do not certify completion.
Task3 provides sidebar paging and projection reads; these remain projections,
not authorization or live workflow confirmation.

## Ordering and constraints

Every repository write locks the owning conversation. DB insertion triggers
also increment the conversation's message/event sequence under that row lock,
held to commit. A writer cannot publish sequence N+1 while N is uncommitted.
Caller timestamps and equal timestamps cannot put a late commit behind an
already-published cursor. This is **commit chronology**, not wall-clock sort.
Retries of event inserts can leave harmless sequence gaps; never assume a
contiguous integer stream. New appends appear when refreshing the latest page,
not while paging through an older snapshot.

Composite foreign keys enforce ownership across turns, messages, attachment
links, attempts, actions, approvals, command targets, summaries, and events.
Messages cannot be updated/deleted. Attachment joins must be inserted in the
message's creation transaction (tracked with Postgres xid8), and cannot be
changed/deleted afterward. Attachment links require ready metadata and at most
four distinct positions. Migrations add listing indexes and durable unique
identities, with no dependencies on Temporal tables or extensions.

`storage.ErrConflict` identifies changed identity payload/unique logical-key
collisions; `storage.ErrNotFound` covers scoped lookups; `chat.ErrInvalid` covers
contract validation. DB constraint violations are returned as pgx `PgError`
(e.g. 23503 foreign key, 23514 check), rather than guessed HTTP responses.
Task 3 should map these deliberately to the documented API contract.

## Verification

```sh
mise exec -- go test ./internal/storage/... ./internal/chat/...
scripts/test-web-integration.sh storage
```

Ordinary root tests need no services. Integration tests are tagged `integration`
and fail without an explicit test database. The runner requires Docker Compose,
mise, and Python 3, and uses the pinned Postgres image, a UUID-named Compose
project/database/volume, and a Docker-selected loopback port. It ignores ambient
application DB URLs. It performs up/down/up, race-enabled repository tests,
application/Temporal isolation checks, then writes a receipt, **restarts the
actual server**, verifies `pg_postmaster_start_time()` changed, and reads/retries
the receipt from a fresh process. Docker can reassign the ephemeral port on
restart; the runner rediscovers and revalidates it.

Cleanup stops/removes only that generated project, verifies the exact volume's
Compose ownership label, and removes only that test-created volume. There is no
`down -v`, shared service stop, developer-volume removal, or `.data` access.
