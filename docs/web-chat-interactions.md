# Durable browser interactions (Task12)

The UI extends the [streaming client](web-chat-streaming.md) and uses the
[private API](web-api.md) verbatim. It does not authorize tools or write SQL.

## Commands and recovery

- Send is a normal FIFO follow-up even while another turn runs. Replace is an
  explicit separate action and captures **metadata.activeTurn.id**, as does Stop.
  Queued turns and optimistic drafts are never control targets.
- A saved replacement contains its exact text, attachment IDs and expected target.
  Retry/reload cannot redirect it to a newer active turn. The same request ID and
  exact payload are used after uncertainty; the server computes the digest.
- Stop, decision and revoke use a per-conversation delivery journal. Pending
  receipts are polled by command ID even outside the bounded snapshot window.
  Local intent is not an execution or policy projection. Late callbacks are
  fenced by mounted session and immutable request ID, and terminal receipts do
  not regress to pending.
- HTTP202 means saved intent. `resultState:accepted` means workflow admission,
  not completion. The UI says “admitted by execution; see saved work state.”
  Asynchronous rejection stays visible. Cancellation waits for actual cleanup
  joins; a held/missing acknowledgment is not treated as cancelled or replaced.
- Message-size413 correction keeps the Task11 typed pre-acceptance exception;
  other uncertain message/control failures retain their retry identity. Storage
  failure before dispatch blocks new commands. Failure saving a receipt after
  dispatch cannot imply the command was never sent.
- Policy, active work, pending approval and saved history come from the server
  on reopen. Revoke's contract contains requestId only, not an invented version
  assertion. Policy revision/toolset and exact approval/action identity remain
  visible; expired or stale projected scopes disable decision controls.
- Pending approval is outside collapsed work details and precedes old rejection
  notices. Narrow-width tests check actual button bounds inside the scroll panel,
  not just DOM presence. Once covers the exact action/retries; All covers the
  current versioned toolset; revocation does not undo already-authorized work.

## Attachments and media

Picker/drop performs count/type/byte checks, then sends one multipart file per
request. Only a ready response belonging to the original conversation supplies an
ID. Up to four distinct selections are submitted, at most1,048,576 original bytes
each. Server signature/parser errors are actionable, not bypassed by extensions.
A completed upload after navigation saves only into its original chat's draft.
Removing a selection clears local selection only; originals/history are not deleted.

Saved messages expose their canonical attachment IDs with **Attach again**.
This works after clearing local storage: new analysis creates a new action with
its new instruction and needs authorization. Metadata placeholders are deliberate
when a saved ID has no known safe raster type; no new metadata API was invented.

Preview is explicit user activation, never automatic model permission: only a
known ready PNG/JPEG can fetch its conversation-scoped original; response media
must match and include nosniff, and size remains bounded. The resulting blob URL
is revoked on unmount. CSP permits blob **images only**; SVG/HTML, iframe/object,
arbitrary Markdown media, third-party images and source prefetch remain disabled.
There is no SVG browser preview or client rasterizer. Unknown/unsupported media
stay metadata-only. The user preview does not grant the model original access.

The [Task9 image profile](web-chat-image-profile.md) remains unchanged: each image
action uses one640×160 normalized PNG, bounded printable ASCII instruction and
pinned Ollama0.40.15/MLX/model digests, with no optional image history/tools. The
UI exposes these constraints and never rewrites a rejected instruction. General
`imageAdmissionQualified:false`, known larger-Qwen limitations, no OCR/fallback,
and lossy context warnings remain. Image error outcomes are not translations.

## Verification

```sh
mise exec -- pnpm --dir web install --frozen-lockfile
mise exec -- pnpm --dir web test
mise exec -- pnpm --dir web typecheck
mise exec -- pnpm --dir web build
# Install a browser in a task-owned cache if needed; don't alter a shared install.
PLAYWRIGHT_BROWSERS_PATH=/absolute/owned/browser-cache mise exec -- pnpm --dir web exec playwright install chromium
PLAYWRIGHT_BROWSERS_PATH=/absolute/owned/browser-cache mise exec -- pnpm --dir web exec playwright test
```

`tests/chat.spec.ts` routes deterministic HTTP/SSE fixtures through the actual
React app; it is not provider evidence. `tests/chat-live.spec.ts` opts into an
actual executable API/worker/Next stack with isolated Postgres/Temporal and the
existing Ollama provider. No model fixture is used in the live tests. It verifies
real approval/reload/revoke/deny, image follow-up, replacement priority and stale
Stop/Replace races. Race requests are held at the browser boundary then continued
to the real API, not fulfilled with fabricated receipts.

Start the owned runtime in another terminal after building:

```sh
R=$(mktemp -d /tmp/webchat-task12.XXXXXXXX)
caffeinate -is env WEBCHAT_BROWSER_RUNTIME_DIR="$R" \
  WEBCHAT_TEST_STORAGE_RUN='^$' WEBCHAT_TEST_CHAT_RUN='^TestStreamBrowserRuntime$' \
  GOFLAGS='-run=^TestStreamBrowserRuntime$' bash scripts/test-web-integration.sh stream
```

Read `$R/ready.json`, then run (requires both exact models and the existing pinned
`webchat-attachment-parser:1` image; see attachment setup documentation):

```sh
WEBCHAT_TEST_BROWSER_URL=http://127.0.0.1:PORT \
WEBCHAT_BROWSER_EVIDENCE=/absolute/owned/evidence \
PLAYWRIGHT_BROWSERS_PATH=/absolute/owned/browser-cache \
  mise exec -- pnpm --dir web exec playwright test
```

The interactive runtime has a12-minute deadline. `touch "$R/done"` signals only
its owner to stop its children and uniquely named test services. Never stop shared
Ollama or delete `.data`. An independent visual evaluator can use agent-browser
at the same URL before signaling done. Prefer a fresh `.next` build: Task11
observed stale incremental CSS, so exit0 alone is not asset verification.
