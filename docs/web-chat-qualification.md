# Web-chat runtime qualification — 2026-11-11

## Status: progression permitted under explicit user deferrals; not semantic qualification

### Task12 R2 — explicit additional user waiver (current acceptance decision)

**User authorization: “Let's defer Qwen issue and continue work.”** This explicitly
defers the **narrow-profile Qwen parent-response reliability issue**, including the
contradictory refusal and stray `</think>`, in addition to the earlier larger-image
quality issue. Task12 may be conditionally accepted and progression may resume
under this documented exception, subject to the remaining applicable gates and
scoped documentation review. The former R2 progression block is superseded; the
underlying defect remains **OPEN / DEFERRED**, not fixed or semantically qualified.
This records the user decision, not publication, merge or unconditional approval.

The two deferrals are distinct:

- The earlier user amendment below deferred larger-image Qwen failures (`MUNICH`
  at1024×1024 and untranslated French at2048×2048). It did **not** cover Task12 R2.
- This new decision additionally accepts progression despite the observed
  narrow640×160 parent failure: the approved tool returned `hello world`, but the
  saved parent answered `Hello World.\n</think>\n\nI can't directly provide the
  translation without access to the image text…`. The requested translation-only
  final therefore **failed acceptance**. Correct tool output/SQL completion does
  not turn that failed final into a pass.

[Retained immutable fixtures and hashes](../testdata/web-chat/README.md) and the
[completed diagnostic](web-chat-r2-diagnostic.md) preserve the evidence. Cause
remains unknown; the original raw provider wire/token stream was not retained.
Source reconstruction did not establish a serialization/context/parser defect.
Exactly **Qwen1/Gemma1** separately authorized reconstructed parent observations
later returned conforming `hello world`; neither proves correction or recovers the
lost original stream. That observation budget is exhausted. This documentation
continuation makes **zero new inference calls** and authorizes no requalification
or production behavior change.

**Waiver boundaries:** keep strict both-model, exact-turn, full-final assertions
unchanged. Future tests must retain actual pass/fail, raw evidence and nonzero
failure outcomes; do not skip/delete/broaden assertions or relabel a failed result
as green. Report any applicable parent-semantic exception separately from the test
result. This is not a waiver for all Qwen errors, transport, authorization, resource,
context-admission, cancellation/cleanup or durability failures. Exact model tags/
digests, no substitution/fallback/OCR/output rewriting, existing safety and
resource/context caps, narrow-image admission guards and
`imageAdmissionQualified:false` all remain unchanged.

Independent scoped re-review found R1 multi-file navigation and M1 cancellation
**ADDRESSED**, scoped code quality **PASS**, visual round2 **PASS**, and no new
blocking code defect. Its R2 spec block preceded this explicit waiver; it is now a
conditional acceptance exception, not a retrospective semantic PASS. Evidence
precision: component RED issued one upload; retained browser RED issued **two**
before abandoning the remainder. Earlier `ERR_NETWORK_IO_SUSPENDED` cause remains
unproven/not fixed; exact fault-log inventory (M2) remains Task14 work.

The sections below retain their historical qualification chronology; pre-deferral
blocking language is not the current progression decision. See also the
[interaction contract](web-chat-interactions.md).

### Task9 continuation1 — narrow production image profile, 2026-11-12

Selected option1 is implemented: [profile contract and source derivation](web-chat-image-profile.md), [source hashes/configs/runtime identity](qualification/2026-11-12-task9-image-profile-sources.json), and [actual application evidence](qualification/2026-11-12-task9-image-application.jsonl). Exact640×160 normalized raster yields102 Qwen or87 Gemma image tokens including delimiters from pinned MLX source, not candidate reserves or finite semantic probes. Mandatory text/framing/output admission is checked before dispatch; every request rejects runtime/digest/profile mismatch. Actual approved API→Temporal→resolver→production provider→SQL analysis translated PNG/JPEG/SVG on both models before and after worker/store restart (12 image results). New instructions required new approval. No OCR/fallback or larger-Qwen investigation. **Global `imageAdmissionQualified:false` and original failures remain unchanged.** Earlier application-disabled statements below describe the prior partial checkpoint and are superseded only for this explicitly narrow profile.

### Task9 small-image experiments — 2026-11-12 (prior partial checkpoint)

[New raw evidence](qualification/2026-11-12-task9-small-images.jsonl) records six bounded live qualification-only calls: the known640×160 PNG, JPEG95, and a static-vector SVG reconstruction rasterized by the actual constrained helper. Both exact tags translated all three: Qwen `HELLO WORLD` (129 prompt tokens each), Gemma `Hello world` (111 each). Command: `caffeinate -is env WEBCHAT_TASK9_LIVE=1 WEBCHAT_TASK9_EVIDENCE=<new-exclusive-jsonl-path> mise exec -- go test -p 1 -count=1 -timeout=10m -v ./scripts -run '^TestTask9LiveSmallImageExperiments$'`;18.05s test elapsed. One image/tiny prompt/16K context/2000 output/90-second deadline, serial calls; no shared service restart or model substitution.

These observations are separate from authorization, arbitrary image-context admission and semantic generalization. No larger-Qwen investigation was repeated. **Original failures and `imageAdmissionQualified:false` remain unchanged.** Application image translation/restart-follow-up is still blocked by unqualified context admission, not by the deferred quality investigation. Production rejects opaque image requests rather than silently dropping them; [Task9 contracts and concrete completion options](web-chat-analysis.md). Document child/parser/SQL and document worker/store restart evidence are real but are not image restart evidence. All historical sections below remain as originally recorded.

**User amendment (2026-11-11, before Task 2):** “Let's move on even knowing this qwen issue. We can investigate it later.” Investigation of the known larger-image Qwen quality failures is deferred; the Task 1 progression gate is removed for that accepted issue. The historical blocked assessment and all original raw evidence below are preserved. `imageAdmissionQualified:false` remains unchanged. This is not image certification, does not waive downstream context admission/resource bounds, and does not authorize fallback. Task 2 performs no Qwen rerun/investigation.

### Historical Task 1 assessment (before the amendment)

The current-code **small-image semantic** gate and the independent longer-text
incremental-receipt gate pass for both exact required models. Short-image stream
delivery is **inconclusive**, not certified by NDJSON record counts. The broader
image gate does not: `qwen3.5:9b` returned **“MUNICH”** for the clearly readable
1024×1024 French fixture and returned untranslated **“BONJOUR LE MONDE”** for the
2048×2048 fixture. Gemma translated all three sizes correctly. Do not interpret
successful metadata discovery, unit tests, or the small-image run as blanket
vision qualification. No model tag, prompt gate, or approved design was changed
to conceal the failures. Image/context admission remains unqualified; dependent
image work needs a reviewed resolution and live requalification.

This is Task 1, **not** the final UI, API, parser sandbox, or lifecycle. No new
workflow/queue is registered. The legacy Go runtime and `mise run setup` remain;
its description now explicitly says it is legacy. Task 13 owns the replacement
`setup`/`up`/`down` implementation. Never delete `.data/`, stop shared Ollama or
Temporal, or use volume deletion to test persistence.

## Task 2 storage verification (2026-11-11)

Storage foundation is implemented; see [repository contracts](web-chat-storage.md).
`scripts/test-web-integration.sh storage` passed migration **up/down/up**, ten
race-enabled real-Postgres repository/security tests, and separate restart
write/read tests. Final project was
`webchat-storage-38c623acd6ed4c71a342a5b89175dc0d`, with database
`webchat_test_38c623acd6ed4c71a342a5b89175dc0d` and loopback ports 60803 then
60883 after Docker reassigned the ephemeral binding. Actual server start time
changed from `2026-11-11 16:47:55.780172+00` to
`2026-11-11 16:48:03.512412+00`; accepted intent/history survived. Cleanup removed
only this generated project and its ownership-checked test volume; inspection
found no leftover storage-test containers, networks, or volumes.

Serialized `go build ./...`, `go vet -p 1 ./...`, and
`go test -p 1 -count=1 ./...` all passed through mise; legacy tests remain intact.
Missing integration configuration and unavailable Docker both exited nonzero.
Self-review only in this task (no agents); independent review is controller-owned.
This evidence does not qualify Qwen images, API/Temporal execution, or UI behavior.

## Verified pins and provenance

Machine-readable pins are in [`../infra/runtime-pins.json`](../infra/runtime-pins.json).
Frontend versions and transitive integrity hashes are in `web/package.json` and
`web/pnpm-lock.yaml`; Compose images have immutable manifest digests.

Vendor metadata was queried on 2026-11-11, rather than guessing release numbers:

- Node **24.21.0**, LTS Krypton: <https://nodejs.org/dist/index.json>; also the
  actual `node --version`. pnpm **12.18.0**: npm registry and executed CLI version.
- Next.js **16.4.6**, React/React DOM **19.3.1**, TypeScript **7.0.2**;
  `@types/node` **24.10.1**, React type packages **19.3.0**.
- Future frontend test dependencies: Vitest **5.1.0**, Playwright **1.65.0**,
  Testing Library React **16.3.3**, jest-dom **7.0.1**. These are pinned, not a
  claim that frontend interaction tests exist in this shell slice.
- Package metadata: `https://registry.npmjs.org/<package>/<version>` includes
  version, engines, license, and tarball integrity. All installed on Node 24.21.0.
- Postgres **16.14-bookworm**; Temporal server and admin/schema tools **1.32.2**;
  Temporal UI **2.58.1**. Verified via Docker Hub
  `https://hub.docker.com/v2/repositories/<repository>/tags/<tag>`, actual pulls,
  server startup, and SQL/CLI health checks. New server images do not include
  auto-setup or the CLI: schema/namespace initialization uses the matching
  admin-tools image. Temporal config is based on the MIT-licensed upstream
  <https://github.com/temporalio/temporal/blob/v1.32.2/config/docker.yaml>.
- pgx **v5.11.0** and golang-migrate **v4.20.1**: GitHub release metadata and
  `go list -m -json github.com/jackc/pgx/v5@v5.11.0
  github.com/golang-migrate/migrate/v4@v4.20.1`. Pinned in the manifest for Task 2;
  deliberately not added as unused dependencies to the existing `go.mod`.
- Go **1.27.3**, Ollama **0.40.15**, Temporal CLI **1.10.0** are existing pins,
  unchanged. Actual Go and shared Ollama versions were checked. No Go toolchain
  upgrade was attempted.

## Fix round 1: qualification assertion corrections

Review reproduced two false positives in the original runner: fully buffered
multiple NDJSON records passed as streaming, and an explicit refusal containing
“hello world” passed as a translation. The original automatic certification was
too permissive; the historical recordings below are **not** evidence collected
by the corrected code. Original Qwen failure recordings remain unchanged.

The corrected runner distinguishes:

- **Protocol framing** (`chunks`, `text_chunks`) from **body receipt observation**
  (`text_receipts`, `text_receipt_span_ms`, `delivery`). The receipt trace records
  the body-read index and elapsed arrival time completing each non-final text
  record, deduplicating records completed by the same read. An unknown-length
  response with at least two text-bearing reads spanning a broad **250ms
  observation window** can be labeled `incremental-receipt-observed`. Known-length,
  fast, and coalesced observations remain `inconclusive`; multiple protocol
  records alone never establish streaming. The longer text probe exits nonzero
  if inconclusive. This is deliberately conservative and may require another
  observation of a fast genuine stream; it is not a provider failure diagnosis.
- **Receipt is not generation.** The window is an observation sufficiency rule,
  not an inference-speed/GPU heuristic. Network buffering, server pacing and
  client scheduling remain possible explanations; even positive evidence does
  not prove when the provider generated the tokens or released GPU work.
- **Short-image semantics** are independent of delivery: an unambiguous correct
  one-record translation can report `image_semantics: accepted` while its
  `delivery` remains `inconclusive`. The longer text probe separately establishes
  incremental-receipt evidence for each exact model; short translations are not
  forced to have multiple records or an arbitrary minimum generation duration.
- **Fixture-specific semantic acceptance** normalizes case, whitespace, common
  quotation/punctuation and optional `Translation:` / `English translation:`
  labels. It accepts only “hello world” or “hello to the world.” All other words
  remain significant; question marks and ellipses do not disappear. Refusals,
  negation, guesses, extra explanations and unrecognized variants return
  `image_semantics: review-required` and a nonzero result requiring explicit
  review. This is not a general-purpose semantic classifier or an automatic
  rejection of every legitimate translation outside the small acceptance set.

### Historical corrected-runner live rerun (one small-image run, unchanged prompts/tags)

Command: `go run ./scripts/qualify-chat-models.go`, **exit 0**, nine probes.
New evidence:
[`qualification/2026-11-11-fix-1-small-image.jsonl`](qualification/2026-11-11-fix-1-small-image.jsonl).
This was collected with the fix-round-1 code, not retroactively derived from the
three original evidence files:

- Qwen longer text: **29 text-bearing body reads across 1,058ms**, observed
  incremental receipt. Gemma: **27 reads across 465ms**, observed incremental
  receipt. Both were unknown-length HTTP responses.
- Qwen small image: **HELLO WORLD**, semantic acceptance; body-read span **0ms**,
  delivery **inconclusive**. Gemma: **Hello world**, semantic acceptance; span
  **20ms**, delivery **inconclusive**. These results do not certify image-stream
  timing or the supported image envelope.
- Both tool schemas, active HTTP cancellation and explicit missing-model checks
  passed. No larger-image, combined-budget, or broad provider investigation was
  run in this fix round. No prompt/tag changes or downloads.

Regression RED caught both buffered-multiple-record variants (Content-Length
and chunked framing) passing, absence of receipt evidence for a genuinely
flushed/delayed provider, and incorrect rejection of a semantic-only short image.
Separate RED caught refusals, negation, guessing and ambiguous answers passing
substring checks. GREEN: `go test ./scripts -count=1 -v`, repeated
`go test -race ./scripts -count=5`, full uncached serialized
`go test -p 1 -count=1 ./...`, and `go vet -p 1 ./...` all pass.

**Historical pre-deferral acceptance assessment: BLOCKED:** larger-Qwen semantics and combined
image/text/multi-image admission are still unqualified. The PDF font-data warning
remains deferred to the bounded parser helper; it is not resolved by this fix.

## Historical live model evidence (original runner at `24157b6`)

These are actual HTTP calls to shared `http://127.0.0.1:11434`, **not mocks**.
Both models are MLX/safetensors on this host. Model digests are recorded in the
manifest because tags can move. Qwen was downloaded through `/api/pull` with the
exact tag: **7,639,229,431 bytes**, ending with `writing manifest`, then `success`.
Existing Gemma is **9,461,576,343 bytes**. Neither model was substituted or deleted.

Committed raw evidence:

- [`qualification/2026-11-11-small-image.jsonl`](qualification/2026-11-11-small-image.jsonl):
  original runner, exit 0, nine probes (its streaming/semantic assertions were
  later found too permissive, as described above). Text first-chunk/total: Qwen
  **16.785s / 18.649s**, Gemma **6.429s / 6.999s** (cold/model-switch costs included).
  Qwen produced 56 non-final text chunks, Gemma 39. Both generated exactly the
  schema-valid `web_fetch({"url":"https://example.com/"})` call. Nothing fetched
  the URL; this is a model tool-selection test, not tool execution.
- Both translated the 640×160 fixture's **BONJOUR LE MONDE** to English
  **hello world**. The original assertion only checked two keywords and could
  incorrectly accept refusals; it has been replaced. These particular recorded
  answers were also manually inspected, but that does not repair the old gate.
- Cancellation waited for an active streamed token before cancelling the HTTP
  request. Both read loops returned `context.Canceled`, without a completion
  event, in less than 1ms after cancellation (millisecond-resolution `cancel_ms`
  is omitted when zero). Fake-provider testing separately proves the server-side
  request context closes. Live evidence proves client/transport cancellation,
  **not an instrumented GPU-kernel termination or Temporal cancellation join**.
- The deliberately missing tag returned explicit HTTP 404/model-not-found;
  unknown application model tags are rejected before HTTP. Every streamed
  response chunk must identify the requested exact model.
- [`qualification/2026-11-11-extended-1024.jsonl`](qualification/2026-11-11-extended-1024.jsonl):
  extended runner, **exit 1** due to Qwen image translation. All other probes
  passed. Qwen image: **5.097s**, 1,053 prompt tokens, “MUNICH”. Gemma image:
  **2.160s**, 555 prompt tokens, “Hello world”.
- [`qualification/2026-11-11-context-image-sizes.jsonl`](qualification/2026-11-11-context-image-sizes.jsonl):
  additional non-streaming diagnostic probes, not a substitute for the streaming
  gate. Qwen 2048-square image took **29.738s**, 4,125 prompt tokens, and copied
  the French instead of translating. Gemma took **5.772s**, 1,115 prompt tokens,
  and translated correctly. All PNG fixtures are committed under
  `scripts/testdata/`; they are under 1 MiB and visually readable.

### 16K context and image reservations

All runner requests set `num_ctx=16384`, `num_predict=2000`, temperature 0, and
`think=false`. Each long-context probe places an early `orchid-731` marker before
11,000 repeated `x` tokens. Both recovered the marker, reported **11,042 / 11,039**
input tokens, and `/api/ps` confirmed **context_length=16384** for the exact
active model. Initial diagnostic timings were **45.664s / 43.563s**; the later
streaming extended run took **35.291s / 12.953s** (cache state differs).
This tests long text and active configuration, not arbitrary context contents.

The same translation prompt without an image reports **27 / 24** tokens for
Qwen/Gemma. Measured image increments:

- 640×160: Qwen **102**, Gemma **87**.
- 1024×1024: Qwen **1,026**, Gemma **531**.
- 2048×2048: Qwen **4,098**, Gemma **1,091**.

The design's base reserve of **2,384** is demonstrably **not enough for Qwen's
2048 image**. A candidate conservative starting reserve is **4,608 per Qwen
image**, **1,536 per Gemma image**, *in addition to* the base reserve. With one
image, that would limit estimated text to **7,392 / 10,464**, respectively:
`min(12000, 16384 - 2000 - 2384 - imageReserve)`. Two Qwen images leave 2,784;
three cannot fit even with zero text and must be rejected or split, not silently
truncated. Four attachments do not imply four images fit in one generation.

**These are measured lower-bound-derived candidates, not admitted budgets.**
Combined near-limit text+image, aspect ratios, and multiple-image requests still
need live validation; semantic translation already fails on Qwen. Until resolved,
fail closed on an unqualified image request rather than claim a safe reservation.
`imageAdmissionQualified` is explicitly false in the manifest. Token estimation,
actual assembly/admission, safe downscaling, and user-facing rejection belong to
later tasks; this shell contains no image upload or generation route.

### Machine/resource observations

Host: Apple `Mac16,8`, **24 GiB** RAM, about **172 GiB** available disk before
pull. Docker reports **8,321,232,896 bytes**, 12 CPUs. Snapshot model VRAM after
large probes was about **8.86 GB Qwen / 9.81 GB Gemma**; this is not peak RSS.
Keep one concurrent generation; do not assume both models stay loaded. Docker
limits: Postgres 512 MiB, Temporal 1536 MiB, UI 256 MiB, each initializer 256 MiB.
Large-context inference and cold model switching are materially slower than the
small warm probes. No service configuration or shared process was changed.

## Parser/rasterizer selection (implementation boundary)

Selected packages are installed and locked as development qualification tools;
Go remains the execution engine. A later Go document activity can invoke a
bounded helper process. Do not import these parsers into Next request handlers
or treat library options alone as a sandbox.

- **PDF: pdfjs-dist 6.5.438, Apache-2.0**, maintained Mozilla PDF.js. Byte-array
  input only; no URL input, external font/CMap/wasm URLs, forms, actions, or JS
  execution. Use text extraction, not rendering/OCR. Available controls include
  `stopAtErrors`, `useWasm`, `useSystemFonts`, and `disableFontFace`. Use only
  bundled local standard fonts when needed. The spike extracted “Hello document”
  from a generated one-page PDF. Without a standard-font directory it emitted a
  font-data warning but extracted text correctly; configure the local path in the
  real helper. Enforce 100 pages, 200,000 extracted characters, 16 MiB expanded
  data, and reject encrypted/empty-image-only results explicitly. Process timeout
  and OS memory limit are required because parser allocation occurs before some
  page-level limits can be checked. Poppler was considered but not selected
  (native package/license footprint); no handwritten permissive PDF parser.
- **DOCX: yauzl 3.4.0 (MIT) + saxes 6.0.0 (ISC)**. ZIP `lazyEntries`,
  `validateEntrySizes`, `strictFileNames`, and byte-counted decompression streams
  support early abort without unpacking arbitrary paths. Only expected OOXML
  parts are read; count all inspected expanded bytes against 16 MiB, cap entries,
  nesting/text, and ignore macros and external relationships. Saxes exposes
  DOCTYPE for immediate rejection and does not fetch external entities unless a
  caller deliberately adds that behavior. The spike parsed namespaced text and
  rejected an undefined external entity. Do not add custom entity resolution.
  Mammoth-style HTML conversion was not selected: embedded text only is a
  smaller required surface. ZIP-bomb/adversarial wrapper tests remain Task 5.
- **SVG: @resvg/resvg-js 2.6.2, MPL-2.0**, Rust resvg binding. The spike rasterized
  the known fixture with `loadSystemFonts:false` and one explicit local font.
  `imagesToResolve()` exposed an external HTTP image as unresolved; no resolver
  was provided. **Reject** external references before rendering; do not merely
  rely on silently omitted resources. Strict XML allowlist/prevalidation (saxes)
  rejects DOCTYPE/entities, scripts, foreign objects, unsupported constructs,
  external images/fonts and CSS resource references. Rasterize only in a process
  with a **10-second deadline**, bounded output dimensions and OS-enforced memory
  limits; kill/join on timeout. The library is not itself a sanitizer or memory
  sandbox. Honor MPL notices for distributed binaries/modifications.

These maintained implementations expose the required control points. Full
sandboxing, signature validation, 1 MiB input/four-file limits, 20MP/8192 image
validation, chunk limits and adversarial acceptance tests are **not implemented
or falsely certified by this Task 1 dependency spike**.

## Reproduction and infrastructure verification

Prerequisites: existing pinned Go, Node/pnpm, Docker daemon and Compose. Do not
run the legacy setup merely to run these tests; it has its old service behavior.
If pnpm is not installed, the equivalent verified invocation used here was
`npm exec --yes --package=pnpm@12.18.0 -- pnpm ...`.

```sh
pnpm --dir web install --frozen-lockfile
NEXT_TELEMETRY_DISABLED=1 pnpm --dir web build
pnpm --dir web typecheck
go test ./scripts -count=1 -v
go test -p 1 -count=1 ./...
go vet -p 1 ./...

# Read-only rendering of the requested test project name (never booted it):
docker compose -p webchat-test config

# Check ownership and ports FIRST. Use another unique project/port set if busy.
docker compose -p webchat-task1-runtime ps -a
lsof -nP -iTCP:15432 -iTCP:17233 -iTCP:18080 -sTCP:LISTEN
docker compose -p webchat-task1-runtime up -d --wait --wait-timeout 240
docker compose -p webchat-task1-runtime ps -a
docker compose -p webchat-task1-runtime run --rm --no-deps \
  --entrypoint temporal temporal-namespace operator cluster health

# Actual model gates (expect the extended 1024 image probe to FAIL on this host):
go run ./scripts/qualify-chat-models.go
go run ./scripts/qualify-chat-models.go -extended \
  -image scripts/testdata/bonjour-1024.png
go run ./scripts/qualify-chat-models.go -model qwen3.5:9b \
  -image scripts/testdata/bonjour-2048.png

# Safe shutdown of ONLY this project; deliberately no volume-removal flag:
docker compose -p webchat-task1-runtime down
```

Observed: three long-lived services healthy; two initializer containers exited
0; Temporal CLI reported **SERVING**. Published ports are only
`127.0.0.1:15432`, `127.0.0.1:17233`, `127.0.0.1:18080`. Application DB/user is
`webchat`; Temporal owns `temporal` and `temporal_visibility` with user `temporal`.
PUBLIC database privileges are revoked. Connecting as `webchat` to `temporal`
failed with “User does not have CONNECT privilege.” No container names or shared
networks are hardcoded. Local example passwords are not production credentials.
Changing password env values after volume creation does not rotate stored roles.

Persistence verification used a sentinel table in the application DB, then
restarted Postgres/Temporal/UI and re-read `survives-restart`. A second clean
project **webchat-task1-fresh** verified automatic initialization from an empty
volume, then `down`/`up` recreation retained `fresh-volume-survives`. Both projects
were brought down afterward, retaining **webchat-task1-runtime_postgres-data**
and **webchat-task1-fresh_postgres-data**. No existing services were stopped.

Two infrastructure failures were diagnosed rather than hidden:

1. On this Docker Desktop bind mount, `test -x` returned true for a mode-0644
   init shell script, then direct execution failed with permission denied. The
   initializer is now SQL with `psql \\getenv`, avoiding executable mount behavior.
   The first task-owned, partially initialized volume was recovered by executing
   that SQL explicitly after Postgres was ready; it was not deleted. The clean
   second-project run proves no manual recovery is required by the final config.
2. Simultaneous service restart briefly made Postgres unavailable while Temporal
   checked schema compatibility, causing Temporal to exit. The long-lived
   services now have `restart: unless-stopped`; the same restart test subsequently
   passed. `down` still stops only project containers and preserves volumes.

The web shell built and served HTTP 200 on **127.0.0.1:19347** in a temporary
owned process, then was stopped by its launch PID. Port 13001 was occupied by an
unrelated Node process and was deliberately left untouched. Checked text says
“Runtime qualification shell. Chat is not connected yet.” No external fonts,
network UI dependencies, proxy routes, or misleading functioning-chat controls.

## Historical Task1 test-first evidence and remaining gates

The fake HTTP provider tests exercise the real qualification client, with
meaningful RED before implementation: buffered-only text, missing completion,
wrong routing, HTTP/malformed streams, invalid tool schema, wrong translation,
completed-not-cancelled streams, and missing-model errors were all wrongly
accepted by the initial stub. GREEN followed implementation. Additional context
RED tests caught missing filler/marker checks and missing exact active 16K
validation, then passed after implementation. No fake results are labeled live.

Final uncached serialized `go test -p 1 -count=1 ./...` and `go vet -p 1 ./...`
passed, including the existing `internal/config` fixture tests. An earlier full
run overlapped the deliberate context RED edit and failed those new runner tests;
it was rerun after implementation. The controller's known parallel Go fixture
3-second timeout was not modified. Frozen pnpm install, build, typecheck, shell
HTTP smoke, Compose config, fresh boot, health and persistence passed.

Remaining blocking work: resolve/requalify Qwen image quality on the representative
fixtures and establish enforceable image+text/multiple-image reservations. A
transport cancellation check is not proof of server compute cleanup. Final
parser sandboxing and final lifecycle/UI remain explicitly later tasks. The
controller's independent review identified the two assertion defects corrected
above. The fix was self-reviewed here and awaits the controller's scoped re-review.
