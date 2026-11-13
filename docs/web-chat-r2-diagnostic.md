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
