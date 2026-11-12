# Database-backed Temporal text chat (Tasks 4–5)

This is a new runtime, not an adaptation of the terminal/project agent's prompt.
Legacy entry points, workflows, tools, files and queues remain unchanged until
Task 13. There is no TypeScript agent loop, workdir, project gathering, fixture
routing, attachment access or tool execution on this path.

## Run

Apply application migrations with `web-api -migrate` (now version **4**), then run
`cmd/web-worker` and `cmd/web-api` with the private API/DB configuration described
in [web-api.md](web-api.md). Build with `mise exec -- go build -p 1 ./...`.
Additional configuration:

- `WEBCHAT_TEMPORAL_ADDRESS`: default `127.0.0.1:7233`, shared by API and worker.
- `WEBCHAT_OLLAMA_URL`: worker-only explicit HTTP(S) URL, default
  `http://127.0.0.1:11434`. No environment-selected model/provider fallback.
- The public model allowlist is still exactly **qwen3.5:9b** (default) and
  **gemma4:e4b**. Every inference request uses the conversation's stored model.
- `web-worker -test-mock` is an explicit test-only provider selection, permitted
  only with a `webchat_test_` application database. `mock` is not an API model.
  Production does not silently fall back when Ollama or a model is unavailable.

`worker.StartWeb` registers `WebChatSessionWorkflow` and persistence activities
on `web-chat-agents`, and `ChatModel` on `web-chat-llm`. Inference concurrency is
one per worker. `web-chat-documents` is reserved, **not polling or advertised as
ready** until a real child is implemented. Child/tool registration is not done
here. Updates are named `message`, `stop`, `replace`, `decide`, `revoke`; query
`state` returns `WebSessionState` without database/network access. Stop and text
replacement execute as described below. Approvals/revocation and messages or
replacements with new attachments still return `invalid_command`, not fake
success or attachment authorization. Rejected message/replacement intent turns
are marked failed with their rejection code; terminal receipt reconciliation does
not rewrite turn projections.

## Admission protocol: no lost enqueue window

The gateway implements `dispatch.SessionGateway.Submit(ctx, Command)` using
Update-With-Start, persisted **CommandID as UpdateID**, original request/digest/
turn, and workflow ID `web-chat/<conversation UUID>`, never a pinned run ID.
It reuses an open execution and refuses to silently restart a closed one.
`client.NewWebGateway(sdk, AdmissionRepository)` borrows permanent receipt lookup
and terminal reconciliation. After a submission error, confirmed terminal status
and an existing receipt replay that receipt, including from an older run.
**Receipt absence is not proof of non-admission.** For an absent marker, query the
exact closed run's replayed active turn/inbox, then call
`ResolveClosedChatCommand(ctx, command, enqueued, observedDecision...)`. Its conversation-locked
transaction combines that enqueue evidence with durable execution state, rechecks
any late receipt, and saves the truthful resolution before dispatch settlement.
The pinned-run query also preserves the exact decision of a control claim,
in-flight receipt or receipt-phase checkpoint. This prevents an unreceipted stop
or rejected control from changing outcome after termination. An enqueued/executed
command stays `accepted` (not necessarily completed); only a
command with neither enqueue nor execution evidence gets
`rejected/conversation_closed`. Late admission writers must agree with this
immutable receipt or conflict. No watermark-only inference is used.
Unknown/running/CAN status, unavailable terminal query or unavailable SQL remains
ambiguous/retryable. A worker must be available for missing-receipt terminal
queries; there is no unsafe negative answer when that evidence is unavailable.
Reuse rejection and Task3 receipt/claim fencing are unchanged. Reconciliation
does not repair terminal workflows or rewrite their turn projections.
`dispatch.Command`/`CommandResult` are aliases for transport-neutral
`chat.SessionCommand`/`AdmissionResult`, avoiding a workflow→storage import cycle.

The API transaction already inserted the user prompt, turn and command. The
workflow never appends that user message again. For each serialized handler:

1. Validate bounded input and its configured conversation/model/digest. The
   `CheckChatCommand` activity verifies every identity against the persisted
   outbox envelope. It returns an existing `commands.execution_receipt`, if any.
2. A saved receipt is returned unchanged. Otherwise admit a normal message into
   the bounded ID-only inbox and advance its sequence watermark **without a
   yield**. Unseen lower sequences are rejected, not falsely acknowledged by the
   watermark. The dispatcher serializes the normal lane until admission; controls
   may bypass that lane but never advance its watermark in this slice.
3. Only **after enqueue** schedule `RecordChatAdmission`. Its scheduling event
   and deterministic enqueue are committed in the same workflow task before the
   activity can write `commands.execution_receipt`.
4. Wait for the durable receipt activity, then return the admission result.
   If a check/receipt batch exhausts transient retries, record a bounded
   `AdmissionCheckpoint` **before** completing the Update as `pending` and force
   Continue-As-New. This checkpoint holds validated command identity/digest,
   eligibility, phase (`check`/`receipt`) and decision, never prompt text. At most
   101 already accepted handlers can hand off (100 normal-budget admissions plus
   one reserved active-target control); new Updates are backpressured.
5. The next run resumes checkpoints through `ResumeChatAdmission`. A successful
   check precedes workflow adjudication/enqueue; only a replayable receipt-phase
   checkpoint can write the receipt. Each batch/phase consumes the run budget,
   permitting repeated rollover even while SQL remains unavailable. The same
   Update ID is retried by the dispatcher in the new run to obtain its durable
   outcome. A pending result is never a final acceptance or rejection.

Dispatch acknowledgement is a separate fenced `SettleClaim` write. Its loss
does not lose the execution receipt or change the Update identity. Handoff
completion and checkpoint creation share a workflow task; no handler is simply
abandoned. The forced rollover prevents the old run's cached pending outcome from
being permanent.

This is not a cross-database transaction. A crash before the admission activity
is scheduled leaves no DB receipt and replay repeats the handler. A crash after
its schedule/DB write replays the enqueue. It must **not** mark a command admitted
in the database and then enqueue in a later workflow task: that ordering could
suppress an unexecuted command. `RecordCommandResult` and `SettleClaim` retain
their existing locking/fencing semantics; execution receipt storage is separate.

Continue-As-New occurs between joined turns or exhausted persistence batches,
waits for all update handlers, and carries pending turn IDs, the last accepted
normal sequence and scalar counters, plus at most 101 reference-only admission checkpoints. An active
recovery checkpoint adds only its
turn ID, `start`/`join`/`settle` phase, bounded backoff and intended settlement metadata
(status/reason, no answer text). A settled model call is not repeated on recovery.
There are no live children in this slice. Inbox limit is 64; a run rolls after
100 handled commands/recovery batches (or server suggestion). A pure validator
backpressures new Updates once that run budget is reached while inference is still
active; one extra exact-target control remains admissible so a full budget cannot
prevent stop. The dispatcher retries the SAME Update ID after rollover. A validator
rejection is not cached. An already accepted handler may complete pending only
with the explicit checkpoint/forced-rollover protocol above.
Handlers count against the budget before yielding to their serialization mutex.
No transcript,
image, provider response or growing receipt list is carried. Rejections at the
inbox limit are explicit `invalid_command`, not silent drops. Durable execution
receipts have no TTL; therefore an arbitrarily delayed retry, including after
more than 64 later commands and a new run, cannot enqueue again. The watermark
is an ordering fence, **not** the source of old-command acceptance.

## Stop and atomic replacement

`ReplacementInput` is `{RequestID, ExpectedActiveTurnID, Text, AttachmentIDs}`.
The existing API transaction has already inserted the correcting prompt/turn;
there is never a second user append. Stop carries the expected target too.

After persisted identity validation, a serialized workflow handler claims the
**exact active target without yielding**. A different claim for that target gets
`control_conflict`; an absent/different active target gets `stale_target`. Same
request retries replay the original permanent receipt. Accepted control receipts
include `targetTurnId` and, for replacement, `replacementTurnId`. Acceptance is
intent/admission, not completed cancellation. Normal sequence order is untouched.

The turn-local cancellation context interrupts model/start activities and start
recovery backoff. Every logical activity future is joined before CAN, but a
logical future (including success/cancellation) does **not** join older timed-out
remote attempts. A separate durable all-invocation barrier gates settlement and
all newer turns. Disconnected workflow context is used only to await futures,
not to run unbounded persistence after workflow cancellation. Cancellation projection
writes use normal workflow context and bounded storage batches. The admission
transaction projects `cancelling`; `MarkChatCancelling` is idempotent, and
`PersistTurnResult(cancelled)` atomically settles the target, running attempts,
optional cached draft and `turn.cancelled` event. Receipt handlers finish or
handoff before the control claim is cleared. No replacement starts until this
settlement succeeds. The replacement occupies a separate single priority slot,
not the bounded normal inbox; queued normal follow-ups retain FIFO order.

Every `ChatModel` body registers a fresh invocation UUID **before** cache/provider
work. Registration commits under the same conversation lock as the permanent
per-turn admission seal. It is either counted by the seal or rejected; even a
late or duplicate delivery cannot start invisibly. The body acknowledges its
registration only after provider/persistence return and heartbeat/shutdown
goroutines join. This uses a two-second uncancelled activity-side cleanup context;
missing/failed acknowledgment remains unknown, not success. Panic/Goexit also
leave the invocation unacknowledged; acknowledgment is not a Go defer. Attempt status,
publisher fencing, worker concurrency, and elapsed time are never cleanup proof.

`SealChatModelInvocations` closes admission and counts all missing acknowledgments.
Only sealed zero allows terminal settlement and newer inference. Any positive
count parks `cancelling_interrupted` (or `cleanup_interrupted` after ordinary
model completion/failure), exposes `UnconfirmedModelInvocations`, and leaves SQL
nonterminal. Replacement/FIFO do not start, even after a late acknowledgment.
Operator investigation is required; no automatic reset/reuse is attempted.
A lost acknowledgment may conservatively park work whose body actually returned.
Worker/server restart cannot erase the ledger. The current runtime has no document
children; Task9 must add their cleanup evidence to the same turn-local barrier.
These tests prove application-body cleanup with fake inference, not live-provider
server-side resource reclamation.

Completion linearizes when the returned model result enters finalization (`join`,
then `settle` after the sealed barrier).
Controls racing with that boundary are rejected `stale_target`, including while
that settlement is recovering: its SQL may already be committed with a lost
acknowledgment, so a control cannot safely revoke it. Stop during **start**
recovery is supported and interrupts its backoff. Cancellation settlement itself
recovers in bounded batches and may CAN with `cancel_mark`/`cancel_join`/`cancel_settle` phases,
the claimed command identity, target/replacement IDs and intended cancellation
reason. No live child/activity, text, attachment content or growing receipt list
is carried. A failed seal activity can recover/CAN with `join`/`cancel_join`
metadata; this is explicitly **not** a cleanup acknowledgment and cannot advance
the turn. The resumed workflow must obtain sealed zero first. Once a positive
missing count is observed, the workflow parks rather than rolling over.
Query-only `DecidingAdmission` exposes one in-flight receipt decision
and must be empty on CAN; exhausted writes move to existing bounded checkpoints.

Replacement inference loads the original target prompt and attachment **IDs** as
explicit provenance, followed by the correcting prompt. Both count toward the
same 12,000 estimated-input-token bound. It never reads attachment bytes or
extracted text and cannot authorize them. A cached provider candidate cancelled
before settlement is stored as `resultKind:cancelled, completionState:incomplete`;
it is never a final assistant answer or a completed context exchange. This slice
still does not persist token deltas; unsaved streamed drafts may be absent.

## Inference and completion authority

`ChatActivities` borrows a `ChatRepository` interface and an `llm.Model`.
`TurnRef` is only conversation/turn ID. Activities load the direct `GetTurn`
projection and up to six recent, whole completed text exchanges in **completion**
order. A queued prompt accepted before a priority replacement therefore sees the
replacement's completed exchange when it eventually runs. They never
include queued prompts or failed/cancelled assistant drafts. `ChatModel` checks
`chat_model_results` before calling the provider and persists a complete provider
response there before returning. The stable logical key is the turn ID (one
model step in this text-only slice). Same candidate is idempotent; changed
candidate conflicts. Response text does not enter activity results/Temporal carry.

`BeginChatModelAttempt(ctx, ModelAttempt) (cached bool, err error)` checks the
candidate, registers the attempt, and marks abandoned lower attempts interrupted
in ONE conversation-locked transaction. `cached=true` skips registration and
inference, retaining the winner's owning attempt. The fast cache read alone is
not an ownership fence. For attempt-owned candidates, `SaveChatModelResult` uses
the same lock and only permits a running, latest registered attempt to publish;
a stale remote publisher gets `ErrStaleChatAttempt`. Production `ChatModel` always
supplies that attempt identity (the existing optional-attempt storage contract
for standalone records is unchanged). A losing activity may adopt a different
attempt's canonical result, never overwrite it. Changed payload from the same
owner remains a nonretryable conflict. Final settlement must update/link exactly
one winning running attempt or roll back, rather than report false completion.
Activity retries reuse an already saved model result without another answer.
Heartbeats run independently of token arrival and DB writes. Provider 400/422/invalid input and missing-model errors
are nonretryable; EOF without a final stream-completion record is still an error.
Provider output is capped at 64KiB; unrequested tool calls are rejected.

`PersistTurnResult(ctx, chat.TurnResult) error` is the settlement activity and
repository method. One conversation-locked transaction:

- reads the immutable cached provider output;
- appends the stable assistant final entry (`chat.LogicalID(turn,"final")`);
- updates turn terminal status and settlement digest;
- links/completes the winning running model attempt;
- appends a stable `turn.completed` or `turn.failed` event.

A repeated identical settlement is a no-op; changed outcome conflicts. Both
candidate attempt and final entry ownership are conversation/turn constrained.
No final assistant message or completion event can escape a rolled-back
transaction. Model errors produce a durable failed turn (no final answer);
missing live delta events do not affect history. Live deltas are not emitted in
this slice. Completion counters advance only after settlement activity success.

Storage activities use up to 5 attempts, 10s per attempt, 2m total; model
activities use up to 2 attempts, 10m per attempt, 25m total, 15s heartbeat timeout.
Exhausted transient persistence batches enter a cancellable `recovering` state,
with deterministic backoff (5s, doubling up to 1m), retaining the active checkpoint
and admitted inbox. Batches remain bounded; recovery continues after storage is
restored, including across Continue-As-New. Query state exposes the phase and
backoff; completion counters do not advance before committed settlement.
Admission handlers perform at most one bounded batch per phase, then transfer
recovery to their recorded checkpoint and finish pending. `RecoveringAdmissions`
is the bounded pending-checkpoint count visible in the query. CAN still waits for
all handlers, but their completion no longer depends on restoration of SQL.
Checkpoint recovery consumes budget in the new run and can roll repeatedly during
a prolonged outage; backoff bounds retry rate and rollover bounds per-run history.
Cancellation, declared nonretryable failures and panics are NOT swallowed.
There is **no automatic reset/restart of a terminal session**. Operator recovery
must preserve admitted history; resetting before recorded enqueues is unsupported.
Projections can still lag after permanent defects/termination; new admission gets
an explicit closed-session result, not an endless ambiguous FIFO-head retry.

## Explicit context bounds (before Task 10 summaries)

Text uses a conservative byte-tokenizer upper estimate: one token per UTF-8 byte,
plus 128 tokens per message for framing. System instructions count. Current
prompt + instructions must fit 12,000 estimated input tokens; otherwise the turn
fails `InvalidRequest` instead of relying on server truncation. Add the newest
whole exchanges until the next cannot fit, never split a user/assistant pair.
At most six exchanges are read. Ollama requests explicitly set `num_ctx=16384`
and `num_predict=2000`, retaining at least 2,384 tokens reserve. These are text
bounds, not image qualification. A general-chatbot system instruction explicitly
states limited history coverage and absence of tools/attachments; completion
events report `limitedHistory:true`. Full history remains stored. No summary or
claim of complete memory is made before Task 10.

## Readiness and shutdown

The API uses a lazy Temporal client so DB-backed intent can still be accepted
while Temporal is unavailable. An owned observation loop probes real CheckHealth
and recent workflow/activity pollers for both active queues. `dispatcher` means
transport connectivity, not successful inference. `workersPolling` means a
poller observed within 90s (can be stale for that interval), not an execution
lease or proof the exact models are available. `/models` remains honest/unknown.
`/readyz` requires DB Ping, transport, pollers and running reconciliation.

`dispatch.ErrRepositoryUnavailable` is observed by the API supervisor; it cancels
and joins HTTP/dispatcher and exits with the sanitized error. Empty claim polls
cannot turn failed acknowledgements into recovered reconciliation. There is no
unbounded restart loop. HTTP shutdown, health observation and dispatcher are
joined before closing their clients/pool. Browser disconnect never submits stop.
Worker stop interrupts and joins inference using the SDK worker-stop channel,
records a bounded failed attempt when possible, and reports retryable
`WorkerShutdown`, **not** user cancellation. Queued work resumes on worker restart.

## Verification

```
mise exec -- go build -p 1 ./...
mise exec -- go vet -p 1 ./...
mise exec -- go test -p 1 ./...
scripts/test-web-integration.sh chat
scripts/test-web-integration.sh replacement
WEBCHAT_TEST_LIVE_MODELS=1 scripts/test-web-integration.sh chat
```

`replacement` runs real blocked-provider cancellation barriers, target conflicts,
retry/CAN/priority/stale-stop checks, terminal control-receipt reconciliation, and
actual worker + Temporal-server + Postgres restart while cancelling. The parent
runner owns the restart protocol and retains the same Temporal SQLite history;
Postgres postmaster timestamps prove its restart. Another real test withholds
cleanup beyond heartbeat timeout and verifies `cancelling_interrupted` with no
replacement execution. `TestReplacementAfterTimedOutAttemptDoesNotBypassCleanup`
holds attempt1 after timeout on one real model worker, cancels attempt2 on another
(or cancels during retry backoff), and verifies no terminal cancellation/FIFO/
replacement while the older body is unacknowledged. It also restarts the workflow
worker while the old provider remains held. The normal replacement test asserts
the actual CAN run chain and scheduled-turn order. Inference is explicitly fake;
cancellation, histories,
SQL, server processes and restart are real. SDK unit tests alone are not join
proof. `storage` and `api` runner modes are preserved. `chat` includes them and starts a
pinned real Temporal dev server as an owned child on unique loopback ports, with
its own SQLite persistence; Postgres is an independent UUID Compose project.
Mock inference is explicit and does not contact ambient model services. Live
smoke is separately opted in and uses exactly the two tags on local Ollama.
Tests inspect real workflow histories (7 rollovers/70 model tasks), simulate
lost gateway/DB acknowledgement, lose admission activity acknowledgement before
rollover, retry after durable provider output, restart workers during inference,
pause the actual test Postgres during final persistence, and read history through
the Go API after worker restart. Fix-round coverage additionally exhausts five
actual SQL settlement attempts, carries the active checkpoint/inbox across CAN,
and admits a third command through the real dispatcher after restoration. It also
keeps both admission check and receipt SQL failing across two low-budget CAN
boundaries before restoring storage; retries retain one command/user/answer/event.
Closure barriers hold admission before SQL while execution completes, or while
start SQL is still blocked: dispatcher resolution stays accepted without falsely
reporting turn completion, and delayed admission writes agree with the receipt. A
short-timeout test workflow and two real workers force both overlapping model
attempt interleavings with barriers; the production model/storage activities
produce exactly one answer/event linked to the correct winning attempt.
This is not a mocked Temporal durability claim. Optional
`WEBCHAT_TEST_STORAGE_RUN`/`WEBCHAT_TEST_CHAT_RUN` regexes select targeted tests;
without them, the runner retains its full default suites.
The runner stops/removes only its own child, container/network and verified test
volume; it never touches `.data`, shared services, developer volumes or models.
