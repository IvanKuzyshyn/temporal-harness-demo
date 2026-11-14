# Browser transport and history (Task 11)

## Run and verify

Use the existing local Postgres/Temporal/Ollama setup and migrations through v8.
Run `cmd/web-api` and `cmd/web-worker` with the same server environment, then:

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
pnpm --dir web start
```

Open **http://localhost:13000**. `WEBCHAT_BROWSER_ORIGIN` must be that exact
origin on both API and Next.js. `WEBCHAT_API_URL` defaults to
`http://127.0.0.1:18081`; the API's `WEBCHAT_API_LISTEN` must match.
`WEBCHAT_PROXY_SECRET` is a shared server-only random 32-byte hexadecimal value.
Export these variables into both processes; Next does not automatically read the
repository-root `.env`. Never prefix secrets with `NEXT_PUBLIC_`.

```sh
pnpm --dir web test
pnpm --dir web typecheck
pnpm --dir web build
go test -p 1 -timeout=120s ./...
scripts/test-web-integration.sh stream
```

The stream gate owns uniquely named Postgres and Temporal resources and removes
only those resources. It runs API/security/replay/retention/paging tests plus a
real Temporal worker with a deterministic streaming model. It is **not** live
Ollama evidence. For repeatable interactive evidence against actual executables:

```sh
export WEBCHAT_BROWSER_RUNTIME_DIR="$(mktemp -d /tmp/webchat-browser.XXXXXXXX)"
scripts/test-web-integration.sh stream
# In another terminal, read $WEBCHAT_BROWSER_RUNTIME_DIR/ready.json.
# Open its URL with agent-browser; create conversations with BOTH exact models.
# Finish within 12 minutes, then create $WEBCHAT_BROWSER_RUNTIME_DIR/done.
```

The opt-in test builds and owns API/worker/Next children, uses real Ollama (never
`-test-mock`), writes private child logs, and supplies a clearly labeled synthetic
25-user-message fixture solely for paging/Markdown security. Real conversations
must be created/sent through the UI for provider evidence. The ready file contains
no credentials. Child shutdown is PID-scoped; no shared service is stopped.

## Wire protocol and authority

`GET /api/conversations/{id}/events` proxies the authenticated backend SSE route.
The backend bypasses its ordinary request timeout, flushes each event immediately,
sets `Cache-Control: no-store` and `X-Accel-Buffering: no`, and bounds each socket
write to five seconds. The Next proxy forwards the body stream without collecting
it, cancels the upstream reader on browser disconnect, and never sends a workflow
cancellation. It bounds initial upstream connection/header waiting to ten seconds.

Every `data:` line is an envelope:

```json
{"id":"42","conversationId":"uuid","turnId":null,"stepId":null,"attempt":null,"type":"snapshot","data":{}}
```

The SSE `id:` is the same decimal event watermark. It is a string in the browser,
not a floating-point identity. `Last-Event-ID` must be a canonical nonnegative
int64 decimal; duplicate headers, alternate encodings and query-string cursors are
rejected. Resume is exclusive. A retained cursor with at most 256 subsequent
records is replayed before a snapshot; absent, future, deleted, or overly old
cursors receive `snapshot` with `data.reset:true`. A cursor is scoped by the URL's
conversation, not a bearer credential. The client never reuses a different chat's
EventSource. Retained sequence values can coincide across conversations.

SQL is authoritative. A repeatable-read, read-only transaction reads the cursor,
messages and lifecycle projections together, then **commits before any HTTP
write**. Snapshots recur every second; event polling is every 250ms. Initial and
periodic snapshots restore final text and persisted status without any token
history. A completed model attempt is not itself a saved reply: only an immutable
assistant message is labeled **Saved reply**.

Snapshot data contains:
- `reset`, conversation `metadata` (active turn, pending approval and policy);
- latest 20 canonical `messages`, with exclusive older-page `before` cursor;
- latest 200 turn projections, 200 command receipts, 32 tool records;
- latest attempt for each of the 200 most recent logical model steps.

History is paged independently and is not truncated by these live-detail windows.
Each paged message also includes read-only `turnStatus`/`turnReason`, so an older
failed/interrupted message does not lose its status when its turn leaves the live
window. These fields and context metadata are excluded from immutable write
identity. Individual command receipts remain available via their existing route.
Tool/attempt windows are recent activity, not an exhaustive audit-log API.

Subsequent snapshot comparisons emit canonical `message.persisted`,
`command.result`, `turn.status`, `tool.state`, `approval.state`, and `policy.state`
notifications before the snapshot. These derive solely from SQL and can share its
watermark; the reducer accepts same-watermark lifecycle changes. Their replay
retention is unnecessary because the snapshot includes their authoritative data.
Existing durable `turn.*` execution events can also appear; unknown events cannot
change canonical content. `approval.state` data may be null when no approval waits.

`message.delta` is different: it is an explicitly provisional **cumulative**
preview keyed by conversation/turn/logical-step/attempt. The activity coalesces it
at 100ms, caps preview text at 8,000 UTF-8 bytes, and gives each append 250ms. Slow
SQL/display failures drop previews rather than fail execution. Only the latest
128 delta rows per conversation are retained, atomically with append; durable
lifecycle facts/messages/receipts are not deleted. The final saved message is not
limited to preview size. General event payloads remain capped at 64KiB.

There is no per-browser producer queue. A stream holds at most one 64-event page
plus bounded projection data. Browser drafts have at most one current attempt per
logical step; an authoritative attempt fence rejects old attempts and completed
steps. Final messages or terminal turns clear corresponding drafts. Loaded history
and settled command receipts merge by immutable IDs. A long disconnect that leaves
a missing middle reopens older paging rather than silently hiding that gap.

## Browser behavior and safety

- Sidebar/new chat, only enabled model `qwen3.5:9b`, first-prompt
  titles, chronological paging, bottom composer, and ordinary safe Markdown.
- Conversation switching closes only its reader. Requests are stored locally
  **before** send with stable request IDs. Uncertain delivery is retried with that
  same identity; the snapshot can acknowledge an accepted request after reload.
  Late HTTP responses may settle only their matching pending identity in the
  original mounted session; they cannot clear a newer request or draft.
- HTTP errors retain their status/code. Only `413 prompt_too_large` and
  `413 body_too_large` are treated as definitive pre-acceptance size rejection
  by the message composer: release the matching pending ID, preserve editable
  text and correction guidance across reload, then use a new ID for corrected
  text. These validations precede command acceptance. The prompt limit is
  **65,536 UTF-8 bytes**, not JavaScript characters. Unknown codes, conflicts,
  server failures and transport uncertainty retain the original retry identity;
  other status codes are not blanket-classified as safe to resend with a new ID.
- Queued/thinking/stopping states, concise failures and required approvals remain
  visible. Diagnostic counts, context/provenance, tool/attempt dumps and collapsed
  Details/Work panels are removed. Durable reducer/API records are unchanged.
- History prepending preserves the visible message anchor (including when gap
  recovery inserts rows below that anchor). Each page request captures its cursor
  and pagination generation. A stale response can merge immutable rows but cannot
  advance or close a newer gap-recovery boundary. On exhaustion, a same-height
  beginning marker replaces the paging button to avoid shifting the top anchor.
  Live text follows only while already near the end. Reload restores canonical
  history, never stale
  in-memory token text; unsent drafts are per-conversation local storage.
- Required consent, revoke, failed/cancelled/incomplete output and the reply-length
  warning remain visible. A durable failed tool outcome adds a concise notice to
  its own turn even if that turn completes; raw errors/results are not rendered.
  Partial/omitted-context, lossy-memory, provenance and accounting diagnostics are
  not presented, and there are no collapsed detail sections. Attachment consent
  resolves the exact requested ID through scoped metadata only, showing its name
  and stable upload number (also in saved messages). Loading/failure cannot enable
  permission; Deny retains the normal expiry/scope fences. Upload and explicit
  trusted raster preview controls are implemented.
- Host, Origin and Fetch Metadata are checked on both hops. Browser mutations need
  an exact same origin. No CORS relaxation, browser database access, Temporal
  credentials, or client-supplied proxy authentication is introduced. Incoming
  headers are allowlisted; upstream redirects are not followed.
- Raw HTML is skipped. Markdown images/media render as inert labels: **no remote
  image loads, media embeds, link prefetch, or hidden requests**. Links are plain,
  explicit user-activated absolute HTTP(S) anchors without credentials, with
  `noopener noreferrer` and no-referrer policy. JavaScript/data/protocol-relative
  URLs are inert. CSP additionally restricts connect/image/media/frame/object
  destinations; this is defense in depth, not permission to relax Markdown safety.
- No external font/image/CDN assets are loaded. Trusted raster previews require
  explicit user activation; attachment-name metadata never loads original bytes
  or media. This is not an exception for arbitrary Markdown.

## Historical Task 11 visual reference and licensing

The following records the original Task 11 adaptation, not the current accepted
Task 15 dark layout. It is retained as historical design/licensing evidence.

Inspected live: https://vercel.com/templates/next.js/chatbot and
https://chatbot.ai-sdk.dev/demo. Inspected upstream repository **vercel/chatbot**,
its Apache-2.0 license and chat/sidebar source before implementation. The local
warm-neutral spacing, restrained sidebar, centered reading column and grounded
composer are an original visual adaptation. No upstream component/source/assets
were copied; no hosted auth, storage, analytics, AI SDK execution layer or
stop-on-navigation behavior was adopted. All execution and history remain owned
by this application's Go/Temporal/SQL architecture.
