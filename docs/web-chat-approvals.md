# Recorded web-chat approval and execution (Task 6)

This extends the [session protocol](web-chat-temporal.md), not the legacy coding
agent. Apply migration **000005** with old API/workers stopped. Schema 5 is
required by both executables. There is no old workflow-history compatibility
promise. Do not hot-mix workers with different workflow code/toolset definitions.
The down migration deliberately refuses to collapse multiple model steps into
one turn row; it is not a supported production rollback with multi-step data.

## Authority and scope

The registered set is exactly `web_fetch@1,file_upload@1`, identified by
`chat.WebToolset`. A grant matches that **whole versioned set exactly**, not a
name prefix or a dynamically growing registry. A tool/version addition must
change the set identity; old grants cannot authorize it. Unknown names, extra or
duplicate argument keys, unsupported schemes and malformed attachment identities
fail closed. The [Task7 fetch adapter](web-chat-fetch.md) now implements the
public-address/DNS/redirect policy; canonical URL parsing alone is not an SSRF
defense. Task8 extraction and Task9 document children remain separate adapters.

`chat.ApprovalScope` binds conversation, turn, stable tool-call ID, tool name and
version, toolset version, canonical action hash, immutable content digest and
policy revision. Arguments persist in SQL; URL content references are SHA-256 of
the canonical URL (not a claim the remote response is immutable). Attachment
content references are the ready metadata checksum. Only that checksum is loaded
before approval: no original bytes, extracted text, OCR, derived images or
filesystem paths are read. Upload still does not authorize processing, and new
attachment-bearing messages/replacements remain unsupported until later tasks.

The real `WebChatSessionWorkflow` consumes model-generated action references.
`decide` and `revoke` Updates use the same persisted command identity, identity
check, immutable execution receipt, separate dispatcher acknowledgment, bounded
admission recovery and CAN handoff as messages/controls. `CommandIdentity` now
includes approval ID/hash/decision so receipt-checkpoint recovery cannot substitute
another decision. `DecideInput` documents the public request fields; transport
continues using the complete `SessionCommand` envelope, not an unaudited signal.

One pending approval exists at a time. It expires **24 hours from creation**, not
24 hours from a worker restart or rollover. Once approves just the matching
logical action and its recorded retries. All also increments the revision and
records the exact current set. Deny/expiry create explicit persisted tool outcomes
that the next model step can explain. A revoke increments revision, clears the
grant and invalidates any still-undecided old-revision request; a fresh request
gets a new identity. It does not retroactively cancel already-authorized actions.
A decision linearizes in the serialized Update handler after identity checking,
but execution waits for its durable receipt. All/revoke policy projection and
accepted decision audit are committed in that receipt transaction. Old receipt
retries never roll policy back to an older revision.

Approvals are authorized in deterministic model-call order **before** any batch
execution. Up to four authorized calls then execute concurrently. At most 12
model/tool steps are admitted per turn, reserving a final model response slot.
There is no delegate/explorer, shell, repository or arbitrary-tool fallback.

## Persistence, retries, cancellation and carry

`chat_model_results` now keys immutable candidates by `(turn_id,logical_step)`.
Each model step keeps separately fenced attempts in `model_steps`. Tool calls
have stable IDs derived from turn/step/index, canonical arguments, action hash,
original scope, immutable authorization scope and canonical result owner.
`web_tool_invocations` records every actual tool body, including duplicate
Temporal deliveries, independently of its attempt number. A cached result can be
adopted by a different attempt; its winning owner cannot change its payload.

`ChatStep` is registered under the existing Temporal `ChatModel` activity name.
It wraps inference **and action-reference preparation** in one registered body;
cleanup acknowledgment occurs only after both and all heartbeat/shutdown work
return normally. The direct text-only `ChatModel` Go seam remains available to
older tests, but the web executable uses `ChatStep`. No legacy agent loop is used.

Tool registration checks the exact recorded authorization and the shared permanent
turn seal before adapter work. Publication is fenced against newer attempts and
terminal/cancelling turns. The existing `SealChatModelInvocations` name now seals
and counts **both** model and tool ledgers. Accepted stop/replacement requires
sealed zero across every target body, including older timed-out attempts. Logical
future cancellation, publisher status and elapsed time are not cleanup proof.
Missing evidence parks `cancelling_interrupted`; no newer turn runs. There is no
automatic unpark/reset and no physical provider/GPU reclamation claim.

Ordinary completed/failed turns retain the Task5 fix2 distinction: registration is
sealed, canonical result ownership remains fenced, and old missing acknowledgments
do not permanently veto ordinary recovery. They remain honestly missing in SQL.
Tool activity retry exhaustion fails the turn; it does not silently schedule a
fresh activity with reset attempt numbers. Final failure/cancellation also settles
pending/running tool projections without fabricating cleanup acknowledgments.

Pending approval, absolute deadline, decision, policy, step counters and at most
four action references survive CAN. No arguments, transcript, response or bytes
are carried. Rollover during a pending approval or bounded persistence recovery
has no live tool future; executing batches join all futures first. A failed
approval-resolution write remains attached to its **same** action until resolved,
never reused as consent for the next call. Stale decisions after replacement,
expiry or revision change reject durably, independent of display events.

Pending workflow state can precede the approval creation commit. Cancellation
atomically establishes and resolves that same request, even after creation retry
exhaustion with no commit or a lost acknowledgment. Both creation and resolution
verify the full stored scope, deadline, call/hash and policy revision. Late
same-scope creation cannot reopen a resolved request or change its decision;
conflicting creation/resolution fails, rather than being silently ignored. This
approval tombstone is not invocation cleanup evidence: accepted-control settlement
still passes through the existing sealed-zero barrier.

A completed recorded tool result is loaded as subsequent model context without
reexecution/reapproval. Model context preserves assistant/tool grouping; result
text is bounded to 4,000 bytes combined. The web builder reserves the mandatory
prompt, replacement provenance, current-turn assistant/tool groups and schemas
before selecting up to six whole completed exchanges (newest first, emitted in
chronological order). One final-representation estimate counts JSON-encoded UTF-8
bytes plus 128 per message and the encoded tool schemas against the unchanged
12,000 estimated-input-token limit. Optional history is omitted at exchange
boundaries; oversized mandatory context fails explicitly before inference.
Full results remain in SQL. Limited-history disclosure remains present; this is
not a Task10 summary implementation.

## Task7/8/9 integration seams

- `tools.NewWebRegistry(map[string]tools.WebAdapter)` copies adapters for only the
  two allowed names. `Definitions` exposes their exact schemas. `Canonicalize`
  yields bounded canonical arguments and content reference.
- `WebAdapter` is `func(context.Context, chat.ApprovalScope, json.RawMessage)
  (chat.WebToolResult,error)`. It must honor cancellation and not return until all
  owned work joins. Task7 must enforce fetch policy; Task8 must checksum/ownership
  check the approved original; Task9 must put children under the same joined
  turn-local cancellation barrier. Passing a scope is not permission for arbitrary
  other URLs/files/tools. A future child protocol cannot bypass parent approval.
- Inject adapters via `activities.ChatActivities.WebTools`. Production nil
  registries now install `web_fetch` through `NewWebFetchAdapter(tools.NewFetcher())`.
  With `ChatActivities.Attachments` configured, nil registries also install Task8's `NewWebAttachmentAdapter`; an unset store remains explicitly unavailable. Both production executables use the same `WEBCHAT_ATTACHMENT_ROOT`. See [durable attachments](web-chat-attachments.md).
  Tests may inject network dependencies without weakening the fetch policy.
- `activities.WebRepository` lists the narrow persistence seam: preparation,
  approval/authorization, invocation registration/publication/acknowledgment,
  recorded results and checksum-only metadata lookup. `storage.Repository`
  implements it. Worker registration includes these actual activities.
- API metadata policy now has boolean `approvalAll` plus `toolsetVersion` and
  revision. Pending approval includes canonical arguments, tool name, content
  reference, set version, hash and deadline for a meaningful consent display.
  This is still a non-authoritative SQL projection, not permission to execute.
  Accepted policy receipts include optional `policyRevision`/`grantedToolset`.

## Verification boundaries

`scripts/test-web-integration.sh approval` creates isolated real PostgreSQL and
Temporal servers and runs migration/storage/API/dispatcher tests plus approval
integration. It builds and restarts the actual API executable and restarts SDK
workers while pending and all-enabled; actual history chains prove CAN. Tool
side-effect counters count injected fake adapters, not public network reads.
The tool cleanup case shortens activity timeouts with a test-only interceptor,
holds an older timed-out body, cancels its retry, and verifies strict target cleanup
and worker replay. No cancellation/history/SQL is mocked. Unit workflow timers
exercise the default 24-hour expiry through SDK time skipping, not a 24-hour live
server wait. Existing chat/replacement modes remain available and unchanged in
scope. All runner cleanup is restricted to its generated child processes,
Compose project and ownership-verified volume; developer data is untouched.
