# Durable Web Chat — Proposed Design

**Status:** Approved for implementation by the user on 2026-11-11, including the mise lifecycle amendment. Changes ship as a stack of unmerged PRs; all implementation/review agents use `gpt-6-astra` and receive a new herdr tab.  
**Date:** 2026-11-11  
**Implementation plan:** [Web chat implementation plan](../plans/2026-11-11-web-chat.md)

## 1. Intent

Replace the terminal-first project assistant with a simple, single-user local chatbot. The interface should resemble the [Vercel Next.js chatbot template](https://vercel.com/templates/next.js/chatbot), while Temporal remains the owner of agent execution and recovery. Users should not need to understand workflows to chat.

This is a clean-start application, not a migration of old conversations or running workflows. The existing Go orchestration is the foundation; this is not a TypeScript rewrite of the agent engine.

### Confirmed requirements

- Next.js web interface, conversation sidebar, bottom composer, persistent history, and continuation of previous conversations.
- Two Ollama choices: `qwen3.5:9b` (default) and `gemma4:e4b`.
- Normal follow-ups; cancel active work and start a replacement turn for intervention.
- Tool approval with Approve once or Approve all, scoped to a conversation.
- URL fetching only, not search-engine integration.
- TXT, Markdown, PDF, DOCX, PNG, JPEG, and SVG attachments; maximum file size 1 MiB.
- Image understanding, including translating visible text, using the model directly; no separate OCR pipeline.
- Postgres in Docker stores conversation/turn information and attachment metadata.
- Temporal child workflows, parallel execution, recorded approvals, and long-running activities remain functional.
- Clean start, single-user demo; failure/restart qualification is part of acceptance.
- Three user-facing lifecycle commands: `mise run setup`, `mise run up`, and `mise run down`.

### Proposed implementation defaults

These defaults were proposed during design and accepted with the user's implementation authorization:

- Model fixed at conversation creation; no silent fallback or model switching mid-chat.
- Next.js, Go API, and workers run on the host during development. Docker Compose runs Postgres, Temporal, and Temporal UI; Ollama stays on the host for acceleration.
- Loopback-only services with no login. Next.js is the browser's only API origin.
- Maximum four attachments per message; 1 MiB means 1,048,576 bytes per original file.
- Original attachments and derived images in a persistent managed local directory mounted as a Docker volume where applicable; metadata and extracted text in Postgres.
- Approval expiry: 24 hours. Approve all covers both current tools and can be revoked.
- No rename/delete/export/search UI, regeneration, message editing, branching, sharing, or vector database in the initial release.
- Durable summaries and bounded recent history, targeting a 16,384-token model context.

## 2. Current implementation and disposition

The existing architecture is documented in `docs/architecture.md`. `internal/workflows/session.go`, `inbox.go`, `approval.go`, `toolexec.go`, and `explorer.go` implement durable sessions, cancellation, bounded parallel work, approvals, and child execution. `internal/client/client.go` exposes run-ID-independent Temporal Updates and Queries. `internal/activities/model.go` implements streamed, heartbeating model activity execution. These patterns should be reused and adapted.

The present application is not a general chatbot: it automatically gathers project context, uses local coding tools, stores transcripts in filesystem artifacts, and delivers display events through JSONL files. Its explorer also executes read-only tools outside the parent's approval route. Those behaviors cannot simply be exposed on the web unchanged.

### Retain and adapt

- Go Temporal session/inbox/control primitives, budgets, deterministic scheduling, and Continue-As-New.
- Go Ollama adapter and mock provider, extended for images and request budgets.
- Retry-safe persistence identities, cancellation joining, and authoritative completion semantics.
- Workflow/activity unit tests and durable recovery test techniques.

### Retire from the new runtime

- `internal/tui/`, interactive terminal entry points, terminal-specific dependencies/tests/screenshots.
- `examples/sample-project/`, fixture scripting in `internal/activities/demo_plan.go`, and demo-specific model routing.
- Project evidence gathering and coding-agent prompts.
- Local repository tools, command execution, post-write verification, and automatic repair loops.
- Filesystem transcript/JSONL transport after database-backed replacements are validated.
- Automatic desktop service ownership in favor of explicit development commands.

Keep replay capture utilities if useful, but generate new histories for the new workflow type. Archive the old plans as historical references. Do not claim compatibility with old histories after deleting legacy branches. Never delete `.data/` or stop existing services automatically.

## 3. Service architecture

Browser → Next.js routes → private Go API → Temporal session workflow → activities/Ollama/Postgres/files.

Next.js uses App Router, TypeScript, Tailwind, and shadcn/ui-style components inspired by the reference template. It must not run a second agent loop. AI SDK is optional presentation plumbing only; no AI Gateway, hosted database, or Vercel Blob dependency is required.

The Go API owns application writes and Temporal commands. Next.js does not have an independent database writer/ORM. This prevents two implementations of message identity, approval policy, and submission recovery.

The Go API and worker are independently restartable executables. Use a new workflow type `WebChatSessionWorkflow` and queues `web-chat-agents`, `web-chat-llm`, and `web-chat-documents` to isolate the clean-start runtime.

Docker Postgres holds separate application and Temporal databases/users, including Temporal visibility storage as required by the pinned server. Schema migrations are explicit. All ports are loopback-bound. Pin compatible Node/package-manager, Temporal server, and Postgres versions during the first implementation step; record them and avoid unverified version upgrades to the existing Go toolchain.

### Local lifecycle commands

The user installs mise and Docker with Compose support and starts the Docker daemon. Repository trust (`mise trust`) is a one-time bootstrap prerequisite. Thereafter, the supported workflow is:

```sh
mise run setup  # First use, or after dependency changes
mise run up     # Start the complete application
mise run down   # Stop project-owned services; retain all data
```

- **setup:** check prerequisites, install pinned tools and Go/frontend dependencies, prepare local configuration without overwriting existing values, build the applications, and ensure both exact Ollama models are downloaded. Explain large downloads before starting them. It is safe to rerun. A temporary project-owned Ollama process may be used for downloads and is stopped afterward; an existing compatible Ollama service is reused and left untouched. Setup does not leave the chat stack running.
- **up:** start the project Compose services, wait for Postgres/Temporal readiness, apply application migrations, start or reuse Ollama, then start the Go API, Temporal workers, and Next.js in the background. Check required models and all service health endpoints before reporting readiness. Print the browser URL, Temporal UI URL, and log locations. Repeated invocations do not duplicate processes. Missing setup prerequisites produce actionable instructions rather than a partially working success message.
- **down:** gracefully stop project-owned Next.js/API/workers and any project-owned Ollama process, then stop the project Compose resources. Never stop a pre-existing shared Ollama service or unrelated processes. Preserve database volumes, attachment files, configuration, and model downloads. Never use volume deletion as part of normal shutdown. Repeated invocation is harmless.

Lifecycle helpers track process ownership and validate identity before termination; do not kill by port or unverified stale PID alone. Startup failure cleans up only resources created by that invocation and prints actionable errors. Logs and process metadata live in an ignored project runtime directory. Concurrent lifecycle commands are serialized. `down` suspends processing rather than submitting user cancellation; unfinished durable work resumes on the next `up`. Infrastructure downtime counts toward configured Temporal deadlines, so expired work/approvals may settle as timed out after restart.

## 4. Storage and authority

### Application entities

- `conversations`: UUID, title derived deterministically from first prompt, selected model, timestamps, workflow ID, lifecycle and approval policy revision.
- `turns`: UUID, conversation ID, request ID, kind (message/replacement), replacement target, status, timestamps, failure/cancellation reason.
- `messages`: ordered immutable user/assistant records, turn ID, text, attachment references, completion state.
- `model_steps`: individual model calls, logical step, attempt, usage when available, result references and error state.
- `tool_calls`: stable logical ID, arguments, action hash, parent/child execution identity, status, result/source references.
- `approvals`: request/action binding, deadline, decision, effective policy revision, decision timestamps.
- `attachments`: opaque ID, owning conversation, original name, detected media type, size, checksum, storage key, extraction status, extracted text/derived-image references.
- `conversation_summaries`: versioned summary, covered message boundary, model identity and budget metadata.
- `commands`: durable submission/decision/replacement intents, payload digest, dispatch state, result.
- `events`: ordered durable lifecycle events used for browser reconnection; never raw image bytes.

A user turn is not a model step: one turn may contain several model calls, tools, and child workflows. Store both. Attempts are distinguishable without duplicating the completed logical response.

### Authority rules

Temporal owns execution, cancellation, approval policy, and decisions. Postgres owns durable application content and queryable history; execution-status rows are projections written by activities. Disagreement or projection lag must not authorize execution or certify completion. Workflow IDs/run IDs, turn IDs, and stable entry IDs connect the two.

No direct workflow database/network/filesystem I/O. Persist results through idempotent activities with unique keys such as `(turn_id, logical_step, result_kind)`; record attempts separately. A response is complete only after durable persistence succeeds. Database failure must not produce a successful final event.

Use a transactional command outbox: the API atomically records a user message/turn and command intent before returning accepted. A dispatcher submits that command to Temporal with a stable Update ID. Retry after an ambiguous timeout uses the same ID and payload digest. No cross-database exactly-once transaction is claimed. Reconciliation repairs pending commands and projections after restart. Workflow receipts must cover the command retry horizon; retain durable command deduplication across Continue-As-New instead of relying only on the existing 64-receipt FIFO.

Original files use atomic finalization and checksums. Upload metadata is not `ready` until bytes are durably present. Orphan temporary uploads are reclaimed without deleting referenced files. Backups/recovery require Postgres plus the attachment volume, not one alone.

## 5. API and browser events

Publish contracts in `docs/web-api.md` and shared frontend TypeScript types. All mutating commands use caller-retained request IDs and payload digests. Same ID/same payload returns the original result; changed payload is a conflict.

Browser-facing routes under `/api` proxy these Go API operations:

- `GET /models`: exact allowlist and capability/availability status.
- `POST /conversations`, `GET /conversations?cursor=...`: creation and stable paged sidebar listing.
- `GET /conversations/{id}`: metadata, active turn, pending approval, policy, and projection freshness.
- `GET /conversations/{id}/messages?before=...`: chronological history with stable cursor paging.
- `POST /conversations/{id}/messages`: `{requestId, text, attachmentIds}`; accepts/queues a normal turn.
- `POST /conversations/{id}/replace`: `{requestId, expectedActiveTurnId, text, attachmentIds}`.
- `POST /conversations/{id}/stop`: `{requestId, expectedActiveTurnId}`.
- `POST /conversations/{id}/approvals/{approvalId}/decision`: `{requestId, actionHash, decision}`; decision is `once`, `all`, or `deny`.
- `POST /conversations/{id}/approval-policy/revoke`: durable policy revocation.
- `POST /conversations/{id}/attachments`: streaming multipart upload.
- `GET /conversations/{id}/attachments/{attachmentId}`: safe download/preview with server-selected content headers.
- `GET /conversations/{id}/events`: SSE, resume with `Last-Event-ID`.

Commands return `202` plus stable identifiers and accepted/pending state; acceptance is not completion. Invalid payloads use 400/413/415, missing resources 404, stale controls or conflicting IDs 409, unavailable required services 503. Since dispatch can be asynchronous, a later durable command result may report rejection; the UI displays that outcome rather than leaving a permanent pending spinner.

Persist lifecycle events and batched text deltas, not a database transaction per token. Events identify conversation, turn, model step, attempt, sequence, and type. A restarted attempt replaces its live draft rather than appending a second answer. Reconnection can replay retained events or deliver a fresh snapshot if the cursor expired. Durable messages and workflow state always recover the UI without the complete token stream. Events and pending UI buffers are bounded; slow clients cannot block worker execution.

## 6. Turn lifecycle and replacement

One active turn per conversation. Normal messages submitted while busy queue FIFO. Browser disconnects do not cancel work.

States: `queued`, `running`, `awaiting_approval`, `cancelling`, `completed`, `cancelled`, `failed`. Execution phase is separate from terminal outcome.

Replacement is a durable atomic control intent:

1. Validate the expected active turn; stale requests conflict rather than cancelling a newer turn.
2. Persist/enqueue the replacement and request cancellation of the target.
3. Cancel and join running activities and child workflows; pending approvals become invalid.
4. Mark the target cancelled with any partial draft explicitly incomplete.
5. Start the replacement before previously queued normal follow-ups, then resume FIFO work.

Repeated replacement requests with the same ID have one effect. If two different requests race for one target, only the first is accepted. A stop/replacement cannot retroactively revoke completed external reads or inference. If cleanup cannot be confirmed, report cancelling/interrupted state instead of starting overlapping work.

Context for the replacement includes previous completed exchanges, the original target prompt/attachments, and the correcting message with explicit provenance. Do not inject the cancelled assistant draft as a completed answer. Persisted successful tool results may be reused only with valid matching scope; incomplete results are not evidence.

## 7. Tools and approval

Expose exactly two agent tools initially:

- `web_fetch({url})`: fetch a known public URL.
- `file_upload({attachmentId, instruction})`: analyse an already uploaded conversation attachment; despite its externally requested name, this tool does not itself upload bytes.

Uploads alone do not approve content access. Attachment bytes/extracted text must not enter model requests before authorization. The prompt may include untrusted display metadata, never an implicit attachment-content read. A deterministic pre-model attachment action requests analysis approval for newly attached files, so image handling does not depend on the model guessing an attachment tool call.

`Approve once` binds the current tool call ID, canonical arguments, immutable attachment checksum or URL, and action hash. Its scope includes retries and internal processing needed for that same action, not unrelated later calls. `Approve all` also records conversation consent for the versioned toolset `{web_fetch, file_upload}`. It covers child work, survives restarts, and is revocable. Revocation affects not-yet-authorized work, not already-running operations. The UI explains this and shows an approval-enabled indicator.

Already authorized, persisted tool results may remain conversation context without reapproving every model call. A new analysis instruction on an original attachment is a new call. Cached results are keyed by content/action, never just a filename.

Allow only one pending approval at a time initially, authorizing calls in deterministic order before a bounded parallel batch. Denial/24-hour timeout produces a tool outcome the agent can explain. Cancellation invalidates pending decisions. The UI retains Deny as well as both approval choices.

### Child-workflow boundary

Replace repository exploration with `DocumentAnalysisWorkflow`, started for approved PDF/DOCX/text analysis requiring chunking. The parent authorizes the complete bounded document-analysis action and passes an immutable authorization scope: conversation, tool call, attachment checksum, instruction hash, and budget. The child may extract/read only that approved attachment and call the selected model; it cannot fetch URLs, access other files, or launch arbitrary tools. This eliminates the existing explorer approval bypass rather than rebuilding a second approval UI. Future child-originated tools require a new explicit parent approval protocol.

Child workflows use deterministic IDs, bounded execution, cancellation joining, and parent-close cancellation. Chunk-analysis activities run with bounded parallelism and return ordered results. Ordinary text chats need no child. A fixture must prove real child execution, not just registered dead code.

## 8. Fetch and attachment safety

### URL fetching

Only HTTP(S) GET, public addresses, no caller-supplied credentials/headers, cookies, proxies, or arbitrary methods. Reject URL userinfo and private, loopback, link-local, multicast, unspecified, and reserved destinations, including IPv4-mapped IPv6. Validate every redirect and bind the connection to validated resolved addresses to avoid DNS rebinding. Disable environment proxy inheritance. Maximum three redirects, 20-second timeout, and 2 MiB decoded response body. Limit supported content to HTML, plain text, Markdown, and JSON in v1. Strip active HTML; return bounded readable text, final URL, and truncation metadata. Do not run JavaScript or follow links automatically.

### Files

Validate file signatures and parseability, not extensions alone. Conversation ownership checks apply to every attachment lookup. No path supplied by the model is used as a filesystem path.

- TXT/Markdown: UTF-8 text.
- PDF: embedded text only; encrypted/unreadable/image-only files yield explicit unsupported outcomes.
- DOCX: bounded ZIP/XML extraction; ignore macros/external relationships and reject zip bombs.
- PNG/JPEG: decode/validate, maximum 20 megapixels and maximum dimension 8192 pixels; downscale to a bounded model input (maximum 2048-pixel longest side).
- SVG: no script, network resources, external fonts, or external entity loading; reject unsupported constructs and rasterize in a constrained process with a 10-second timeout and memory/resource limits.

Document extraction caps expanded data at 16 MiB, PDF pages at 100, and extracted text at 200,000 characters. Explicitly report truncation. Chunk analysis is bounded to 32 chunks and fails/explains budget exhaustion instead of claiming full coverage. Choose maintained parsers/rasterizer in a qualification spike and pin them; do not handwrite a permissive SVG/PDF parser.

Serve originals as attachments with `nosniff`; only validated raster previews are inline. Render Markdown without raw HTML and sanitize external links. Treat fetched pages, filenames, and document text as untrusted data, not instructions that can override tool policy.

## 9. Model and context policy

Extend `llm.Message` with opaque image references resolved inside the model activity. Do not place base64 images or full documents in Temporal history. The adapter supplies actual image payloads to Ollama after authorization. Test both exact model tags for streaming, cancellation, tool calls, and image translation; unavailable/unsupported capability is an explicit error, never an undisclosed alternate model.

Target `num_ctx=16384`, maximum assembled text input 12,000 estimated tokens, output cap 2,000 tokens per model call, and at least 2,384 tokens of reserve. These are starting defaults, not a claim that every image fits in the reserve. Token estimates and image costs are model-specific; qualification must establish conservative image reservations and reject/downscale/reduce context if they cannot fit. No silent model-context truncation.

Keep up to six recent completed exchanges when they fit, plus a persisted older-history summary of at most approximately 1,500 tokens. Document/web excerpts use at most approximately 4,000 tokens combined per model request. Instructions/tool schemas are included in the input budget. Preserve message/tool-result grouping when trimming. If summarization fails, report reduced historical coverage explicitly rather than silently claiming complete memory.

Summaries have stable covered-message boundaries and idempotent versioned writes. Full messages remain in Postgres. No vector store initially: select bounded document sections and use child chunk analysis when comprehensive coverage is requested. Images are reloaded only when needed and authorized; textual summaries do not preserve all visual detail.

Initial budgets: 12 agent steps per user turn, four concurrent safe tool/chunk activities, and one concurrent Ollama generation per worker by default to protect local hardware. Parallel fetch/extraction still exercises parallel execution even with serialized inference. Document child execution is capped at 15 minutes; model activities retain independent heartbeats and bounded retries. Failed in-progress inference can restart; durability does not promise exact-token continuation.

## 10. User experience

- Left sidebar: New chat, paged conversations ordered by recent activity, first-prompt title.
- Main view: readable messages, Markdown/code, attachment previews, citations/source URLs, collapsed work details, and clear failures.
- Bottom composer: text, attachment picker, send; model picker for a new conversation.
- Busy state: send a queued follow-up, Stop, or Replace current turn with the draft. Preserve unsent drafts when switching views.
- Approval card: tool, URL/file, instruction/arguments, scope explanation, Approve once, Approve all for this conversation, Deny. Never hide a pending approval inside collapsed details.
- Conversation-wide approval indicator with revoke control.
- Clear uploading, queued, generating, waiting-for-approval, cancelling, cancelled, failed, and completed states. No workflow dashboard is required.
- Reopening restores history, attachment references, active work, and pending approval. User scroll position is not constantly forced to the bottom.
- Accessible keyboard controls and responsive mobile-width layout. Browser tests cover interaction, not only screenshots.

## 11. Durability and security acceptance

Acceptance scenarios must prove:

1. Create a chat with either model, exchange follow-ups, reopen it, and recover context.
2. Default selection is exactly `qwen3.5:9b`; unknown tags are rejected.
3. Closing/reloading the browser and restarting Next.js do not stop accepted work.
4. API restart between command commit and Temporal acknowledgement does not lose or duplicate a turn.
5. Worker loss during generation or document analysis recovers with one completed logical result; live attempts are distinguished.
6. Pending approval and Approve all survive restart and Continue-As-New; denial, expiry, stale decisions, revocation, and child scope cannot bypass policy.
7. Replacement cancels/join children and activities before starting its replacement; queued follow-ups remain ordered.
8. Approved fetches execute concurrently; approved large-document analysis uses a real child and bounded parallel chunk processing.
9. A PNG/JPEG text image is translated by both exact models; SVG follows the safe raster path. Reopened image follow-up works without relying on browser memory.
10. Unsupported/scanned PDFs, oversize uploads, zip/decompression bombs, malicious SVGs, XSS, SSRF redirects/rebinding, and cross-conversation references fail safely.
11. Database downtime cannot create false completion; reconnect recovers from missing stream events.
12. Persisted data survives Compose/service restart with volumes intact. Destroying volumes is explicitly destructive, not a durability scenario.

No authentication does not mean unrestricted HTTP access: bind loopback, reject unexpected Host/Origin and cross-site mutation requests, do not enable permissive CORS, and use a server-side secret between Next.js and the private Go API. A network-accessible deployment is out of scope and requires a separate authentication/security design.

## 12. Open verification gates, not hidden assumptions

**User amendment (2026-11-11, before Task 2):** “Let's move on even knowing this qwen issue. We can investigate it later.” The known Qwen larger-image quality investigation is explicitly deferred; its Task 1 progression gate no longer blocks downstream implementation. Original failures and raw qualification evidence remain authoritative, and `imageAdmissionQualified:false` remains unchanged. This amendment does not qualify image admission, waive downstream context/resource bounds, or authorize model fallback. The stop/report rule below remains applicable to new unapproved failures, not this accepted deferral.

- Exact models must demonstrate image/tool support and acceptable latency on the actual machine. If either fails, stop and report; changing a required model needs approval.
- Parser/rasterizer selection and model image token reservation need measured qualification before finalizing dependencies.
- The Vercel template was reviewed as published reference text; implementation must inspect its live UI/source and license before adapting components. It is a visual reference, not a requirement to import its execution architecture.
- No subagent review tool was available when drafting these documents; the author performed the builder/spec consistency review. Execution now uses explicitly launched Pi agents in herdr tabs, each with `openai/gpt-6-astra`, with task-scoped independent reviews.
