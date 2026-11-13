# Task12 R2 bounded diagnostic checkpoint — OPEN

## Scope and physical-call budget

R2 is an **unresolved narrow-profile end-to-end semantic blocker**, not a UI visual defect and not covered by the prior larger-Qwen-image deferment. No successful tool/SQL/Gemma/later-Qwen sample closes the retained failure.

Before diagnostic execution, `task-12-r2-diagnostic.md` in the controller's canonical report directory declared **zero new physical provider invocations**. Actual count for this checkpoint: **zero per model**. No full image workflow or qualification matrix was run. The new deterministic HTTP server is a fixture, not Ollama. Real browser/API tests only upload/navigate or Stop at attachment preflight approval; no action is approved. A workflow is not one physical invocation: approved child analysis and parent continuation are separate calls.

No production model, prompt, adapter, authorization, image admission, size/output cap, instruction or fallback behavior changed. General `imageAdmissionQualified:false` remains. No output filtering/rewrite or fixture-phrase production rule was introduced.

## Retained observation and provenance

Immutable copies and SHA256s are in [`testdata/web-chat/README.md`](../testdata/web-chat/README.md). Original captures remain unchanged in the sibling controller evidence directory.

- Failed conversation `53a28028-36e8-4206-9cca-972163cf30f1`, turn `afaf31c4-c70f-4ea5-bc9e-8f6bccdca4b3`, attachment `01f6bec0-6f37-4b59-80c0-2a51bd080a5e`.
- Actual prompt: **“Translate the text in the image into English. Reply only with the translation.”**
- Actual canonical tool result: `text: "hello world"`, completed extraction/analysis, `truncated:false`, image profile `ollama-0.40.15-mlx-640x160-v1`.
- Actual saved completed parent begins `Hello World.\n</think>\n\nI can't directly provide the translation without access to the image text.` and continues asking for details/another attachment. This contradicts the tool result and violates translation-only instructions.
- Prior different Qwen conversation has DOM-only `HELLO WORLD`; retained Gemma history has `Hello world.`. Their conformity is not causal evidence for the failing turn and does not qualify Qwen.
- Retained worker log (`r2-retained-worker.log`, full copy with matching SHA256) records preflight ChatModel at 10:30:37, approved child `document/0be216c9-3377-51f0-9c60-b985d7d1384d/0d6d39f18c211d6205eacc4ee336adab35a12274d34543f45ce140f8128f43a8` / `AnalyzeAttachmentImage` at 10:30:59, parent ChatModel at 10:31:04, settlement at 10:31:07. These are workflow/activity events, **not raw provider request/response captures**.

## Acceptance strengthened, not requalified

`web/test-support/image-acceptance.ts` checks the entire saved final for the exact submitted turn and unchanged prompt. For the existing BONJOUR LE MONDE raster, only case-insensitive `Hello world` with optional final period/exclamation and surrounding whitespace passes. It does not strip delimiters, disclaimers or additional content. This fixture-specific rule exists only in test code.

Both-model cases in `web/tests/chat-live.spec.ts` now call this assertion for the initial image and reopened attachment follow-up, separately from completion/approval/identity evidence. Model-specific evidence is captured before the assertion and names do not collide. **Those provider-invoking cases were not executed in this checkpoint.**

Running the assertion directly against the retained Qwen history exits **1** (`r2-retained-qualification-fails.log`). The three regression tests reject that failure, accept retained conforming outputs, and reject wrong-turn/additional-commentary cases (`r2-acceptance-regressions.log`). A green negative regression means the validator detects the known defect, not that the defect was fixed.

## Source-backed boundary reconstruction

`internal/activities/web_parent_diagnostic_test.go` uses the actual `ChatStep` → `webRequest` → context builder → bounded Ollama adapter against a local HTTP fixture. The repository seam supplies retained user/tool data and first-turn empty history. Captured request/body, canonical tool serialization, pinned renderer output, synthetic response chunks, parser cases and saved result are in `task-12-fix1-evidence/r2-reconstruction/parent-reconstruction.json`.

Observed in this deterministic reconstruction:

1. The assistant `file_upload` call and tool response share deterministic ID `0be216c9-3377-51f0-9c60-b985d7d1384d`. The retained child ID independently contains the same ID. User text and attachment identity are unchanged. The four wire messages are system, user, assistant tool call (empty narrative), and tool result. Tool name and `tool_call_id` are present; schemas remain `web_fetch` and `file_upload`.
2. The reconstructed compact tool JSON retains the complete analysis/attachment/extraction metadata and `hello world`; no clipping or replacement occurs. `webRequest` marshals the typed result after `context.Excerpt`; eleven text bytes do not approach the excerpt budget.
3. Context accounting exactly matches the retained parent: conservative input3356, image0, output2000, reserve2384, total7740. There are no earlier exchanges/evidence/quotations. `sourceLimited:true` is the normal coverage flag, not proof that this tool text was lost. The parent is a text continuation; no image bytes belong in that request.
4. The adapter emits MLX, streaming, `num_ctx:16384`, `num_predict:2000`, `truncate:false`, `shift:false`, **omitted `think`**. The image-child path (`image_profile.go`) is a distinct envelope and explicitly sends `think:false`. The text parent preserves provider defaults; this difference is real source behavior, not an assumed equal setting.
5. Pinned Ollama0.40.15 source routes MLX through rendered chat. Its Qwen3.5 renderer preserves the tool result inside a user-role `<tool_response>` block and opens the parent `<think>` block by default. Its parser defaults to thinking enabled when nil; the server also resolves model/renderer thinking metadata. The test directly characterizes the pinned renderer/parser, **not the removed live model configuration or original generated tokens**.
6. Application adapter `ollama.go` appends only `chunk.Message.Content`; it does not append the separate `Thinking` field or remove embedded tags. `runChatModel` passes that content to `SaveChatModelResult`; completed history reads the saved final, not display deltas. Supplying synthetic NDJSON with separate synthetic reasoning and the retained bad answer split across content chunks saves the bad answer byte-for-byte and excludes the separate reasoning.
7. Pinned parser characterization: treating the retained string as a raw stream with a single closing delimiter moves `Hello World.` into thinking and leaves the contradictory prose as content. A synthetic stream with an earlier reasoning close followed by the retained bad string preserves the **additional** closing tag and all contradictory prose as content. This demonstrates a possible segmentation path, **not which stream the original provider emitted**.

The reconstruction passes, including targeted `go test -race`. It is not original wire evidence. Original NDJSON, raw generated tokens and an exact contemporaneous provider configuration snapshot are unavailable; the owned original DB/Temporal runtime was removed. Repeating a sample cannot recover those observations.

## Causal assessment and controller options

**Cause remains unresolved at the provider generation / provider parsing boundary.** The leading hypothesis is an inconsistent parent continuation, possibly including an extra reasoning delimiter; another possibility is provider thinking configuration/segmentation differing from expected defaults. Available source and deterministic capture tests do **not demonstrate an application tool-identity/serialization/context-loss defect**. They also cannot prove that the original live request/stream matched every reconstructed byte. A correct child plus faithful storage is insufficient semantic acceptance.

Concrete general next options, none implemented or automatically authorized:

- **Read-only parent-boundary instrumentation first (lowest behavior risk):** capture the exact serialized request, raw provider NDJSON and separated thinking/content at the existing boundary in an owned diagnostic runtime. A controller-authorized direct parent request costs one physical call, not an entire image workflow. It can locate the observed boundary but a passing sample cannot close the retained failure. Raw generation-token capture may need separately authorized upstream instrumentation to distinguish generation from Ollama parsing.
- **Explicit general parent thinking policy:** consider `think:false` (or an explicitly supported default) for parent continuations only after capture identifies a thinking mismatch. This is a production behavior change requiring controller approval and both-model/tool/safety regressions. It does not inherently fix contradictory prose and must not strip output or retry until green.
- **General authorized-tool-result framing:** if exact request/stream shows the provider disregards correct tool evidence, consider a general system/tool-contract clarification that distinguishes available analysis text from unavailable original pixels while retaining untrusted-data boundaries. This changes prompt semantics, requires cross-tool/attachment safety and bounded-context qualification, and cannot hardcode this fixture or rewrite the user's instruction.
- If a provider parser defect is established, pursue an upstream/general correction with pinned-version/profile requalification; simply hiding a delimiter would leave the semantic contradiction unresolved.

The controller must choose the next investigation/correction and acceptance gate. Task12 remains unpublished and Task13 unlaunched. No self-approval, semantic PASS or waiver is claimed.

## Reproduce without provider calls

From the repository root:

```sh
mise exec -- pnpm --dir web exec vitest run test-support/image-acceptance.test.ts
WEBCHAT_R2_EVIDENCE=/absolute/owned/evidence \
  mise exec -- go test -p 1 ./internal/activities -run '^TestRetainedNarrowQwenParentPathDiagnostic$' -count=1 -v
mise exec -- go test -race -p 1 ./internal/activities -run '^TestRetainedNarrowQwenParentPathDiagnostic$' -count=1
```

For model-free real upload/navigation/Stop verification, use the explicit test selection in [web-chat-interactions.md](web-chat-interactions.md). Do not run the full live suite under this checkpoint's budget.

## Capture1 — separately authorized parent-boundary observations

The controller subsequently authorized **one physical parent inference per exact model**, not an image workflow. A predeclaration was appended to canonical `task-12-r2-diagnostic.md` at **2026-11-13T10:54:51Z**, before either call. The original zero-call checkpoint above remains historical; this new capture used **Qwen1 + Gemma1**, then stopped. No returned tools were executed; no image-child, attachment read, workflow, retry or alternative prompt/parameter was used. Historical **R2 remains OPEN**.

### Frozen inputs and current metadata

Full outgoing bodies, metadata and results are under canonical `task-12-r2-capture1-evidence/`. The actual activity/context builder exports an `llm.Request` through a test-only recorder. Preparation runs the normal adapter against local metadata/response fixtures, freezing its exact serialized body. Qwen JSON equals the previous reconstruction's wire. Gemma is equivalent post-tool input differing only by model, explicitly **comparative**, not a claimed original Gemma request. Neither is the missing original live wire.

- Qwen intended/outgoing SHA256: `a3a1cb169b90a3eb3b6074195c751c51ec0921081319c5b72a5e74b48d9da91d`.
- Gemma intended/outgoing SHA256: `41cf18d8219c5039739e46e6064f2a97a9d36724c5e3909fe133f6567163cc8f`.
- Full original instruction, four messages, complete canonical tool result and tool identities/schemas retained. MLX, streaming, num_ctx16384, num_predict2000, truncate=false, shift=false, think omitted, adapter response cap65536. Conservative input bounds Qwen3356/Gemma2588 fit the unchanged budget. No image bytes are included.
- Read-only version/tags/show metadata confirmed Ollama0.40.15 and both exact pinned safetensors/MLX digests. Both show **thinking.default=true** with [false,true] supported. Qwen renderer/parser=qwen3.5; Gemma renderer=gemma4-small/parser=gemma4. Full sampling defaults are preserved in show snapshots and the predeclaration; none were changed. Each adapter call rechecked version/tags, matching the frozen metadata bytes.

### Observed results, not qualification

**Qwen:** one HTTP200, 10:56:05.518681Z–10:56:17.582994Z (12,064ms). Complete raw NDJSON584 bytes/3 records; separate thinking0 bytes, content11 bytes=`hello world`, no tool calls, done=true/reason=stop, provider prompt count750/eval count4. Raw SHA256 `700bfd7b105244055a901bcdc4589c313b4ca1cb6284d0634b8e1d543ebbf377`. Adapter content exactly equals concatenated raw provider Content. Existing unchanged full-final semantic assertion: **PASS for this observation only**.

**Gemma:** one HTTP200, 10:57:06.840908Z–10:57:13.448351Z (6,607ms). Complete raw NDJSON8798 bytes/61 records; 58 thinking chunks/269 thinking bytes, content11 bytes=`hello world`, no tool calls, done=true/reason=stop, provider prompt count514/eval count64. Raw SHA256 `53c5f18f37eab5e090f5d863fa496eb6e18e45f55c438ee72578a1cf34b20ac4`. Adapter excludes the separate thinking and exactly preserves provider Content. Same unchanged semantic assertion: **PASS for this comparative observation only**.

Each model directory contains `intended-request.json`, `predeclared.sha256`, durable `invocation.json`, `outgoing-request.json`, `observed-{version,tags}.json`, `provider.ndjson`, `http-result.json`, `decoded.json` (including individual thinking/content/tool-call/done/metrics fields), `thinking.txt`, `content.txt`, `adapter-result.json`, and `semantic-assertion.json`. `capture-audit.json` verifies exact body hashes, metadata equality, terminal done, no tools, complete HTTP bodies and byte-identical adapter content. No captured failure/result was discarded. Historical failed evidence remains immutable.

### Supported conclusion and next decision

No current request-default/configuration or application parsing discrepancy was observed. The current metadata supports the reconstruction's default-thinking assumption. Qwen emitting no separate thinking **does not prove thinking was disabled or misconfigured**: metadata/defaults and emitted reasoning text are different observations, and raw generated tokens remain unavailable. Gemma's separate reasoning was correctly excluded from the final.

Both current finals are already conforming at the raw provider Content boundary. These samples therefore do not locate the historical nonconformity, establish a parser defect, prove a context/prompt defect, or distinguish old provider generation from old provider parsing. Differing retained/current outcomes are consistent with variability, **not a causal proof**, because the historical raw wire/token stream is missing. Nothing supports adopting think:false, reframing prompts or stripping output as a fix on this evidence alone.

Smallest next general option: controller-authorized passive exact boundary capture on a future qualification attempt, preserving any naturally observed failure rather than repeating this sample until a desired result appears. Any new inference budget or deeper upstream raw-token instrumentation needs a separate decision. If a failing NDJSON already contains the contradiction, investigate generation/provider parsing; only if application output differs from Content does an application adapter correction become evidence-backed. No behavior correction is implemented here.

### Test-only instrumentation and resource audit

`web_parent_capture_test.go` is opt-in and skipped in ordinary tests. It compares outgoing bytes against the predeclared body/hash, consumes an exclusive/fsynced per-model ledger **before** network I/O, refuses reruns against that ledger, disables idle connection reuse/body replay and redirects, and permits only the pinned loopback metadata/chat endpoints. It never has a workflow/tool executor. Raw bodies are buffered **only in the test wrapper** before unchanged adapter parsing (120s deadline, 8MiB artifact safety bound); this does not claim normal streaming latency behavior or alter model options. Errors/empty responses consume the budget and are preserved, not retried.

Deterministic guard tests cover changed-body rejection and one-shot fencing after success or network failure; source reconstruction and guard tests pass under `-race`. Serialized Go suite, three retained semantic-acceptance regressions, and frontend typecheck pass. The live capture test is explicitly skipped in subsequent deterministic checks. The semantic assertion command preserved Node's MODULE_TYPELESS_PACKAGE_JSON warning; no warning/error suppression or package-type behavior change was made.

No new long-running runtime, browser, database or container was started for these observations. Test HTTP transports close on return; both calls completed and the test processes exited. Shared Ollama was not stopped/reconfigured/traced; `.data` and original attachments were untouched. Owned evidence/ledgers remain. R1/M1 source stays at the previously committed implementation; Task12 unpublished, Task13 unlaunched, controller review pending.
