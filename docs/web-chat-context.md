# Bounded context and durable memory (Task10)

**Continuation2 retains bounded, explicitly attributed original user/assistant
quotations alongside separately labeled generated notes.** The unchanged main
both-model memory scenario and captured-refusal regression now pass in one live
qualification run. Generative summaries can still be wrong; this is not universal
memory fidelity or model-policy reliability. All earlier failures remain recorded,
including the original two runs:
`task-10-live-context.jsonl` and `task-10-live-context-data.jsonl` in the controller's
dispatch directory. Gemma had promoted a contradictory assistant interpretation
into its summary after the canonical image result left recent history. The fix
keeps bounded approved observations separately attributed alongside lossy memory.
“Canonical” means immutable recorded output, **not guaranteed factual truth** of
image/document inference. No semantic waiver, fallback, cap increase or image
profile expansion. See `task-10-report.md` and the new exclusive
`task-10-live-canonical-evidence*.jsonl` and `task-10-c2-*.jsonl` artifacts.

## Production assembly and admission

`internal/context.Builder.Build(ctx, conversationID, turnID, budget)` loads a
bounded SQL snapshot and optionally advances durable older memory. Its mandatory
request and invocation attempt are supplied by `ChatModel`. `Assemble` is the
shared final admission path for ordinary/web chat, summaries, document chunks
and the **separate Task9 image profile branch**. It is not a detached estimator.

Default limits are context16,384, estimated text input12,000, output2,000,
reserve2,384, at most six completed recent exchanges, summary bound1,500,
and readable evidence excerpts4,000 UTF-8 bytes shared across current tool groups
and separately retained results AND original quotation pairs (and any legacy
injected recent-result seam). All sublimits remain subordinate to the final total. Mandatory
system instructions, current prompt, replacement provenance, schemas and complete
current assistant/tool groups are checked before optional history or summary
inference. Oversized mandatory input fails; it is never silently clipped. Recent
exchanges are evicted whole, oldest first; assistant/tool pairs are indivisible.
Current groups consume the excerpt allowance first. Up to four independently
retained observations are admitted next, then up to four whole original quotation
pairs, then optional generated notes/recent exchanges. Quotes get no new budget;
if either member cannot fit, the pair is omitted whole, never misleadingly clipped.
An evidence candidate that cannot fit the final total is omitted whole; excerpt
clipping preserves identity/status and reports truncation. Older summary/evidence
precede recent completed human corrections, whose distinct attribution remains. Output exhaustion uses both
provider `done_reason=length` and the reported count reaching the output cap.

`ollama.TextInputBound` renders the **actual pinned** Qwen35/Gemma4 renderer and
schema/tool scaffolding under both thinking defaults. It takes the larger of:

- JSON bytes plus framing (a secondary conservative constraint, not tokenization);
- rendered source-bound byte upper bound plus512 framing/BOS allowance.

For Qwen, per-rune NFD bytes bound NFC normalization expansion even across
added-token boundaries. For Gemma, each ASCII space reserves three bytes for its
space replacement. Neither pinned pretokenizer adds prefixes; byte-level BPE or
byte fallback cannot produce more tokens than the corresponding byte bound.
Summary text uses the conservative union of those normalization bounds.
This is explicitly a **source-derived estimate, not a measured tokenizer count**.
The tokenizer/model/runtime source identities are those recorded in
[Task9's source manifest](qualification/2026-11-12-task9-image-profile-sources.json).
Runtime0.40.15, exact model digest, local safetensors and MLX runner are checked
before every bounded inference. Unknown/mutated identities fail closed. Wire
requests explicitly set `truncate:false`, `shift:false`, `runner:mlx`. Summary
requests alone also explicitly set `think:false` to avoid spending their unchanged
2,000-token output cap on hidden reasoning. Other text calls retain their default;
the fixed image envelope is untouched. The estimate still bounds both thinking
renderings, and exhaustion/blank-output rejection remains mandatory.

[The Task9 image profile](web-chat-image-profile.md) is unchanged: one640×160
image, fixed system envelope and bounded ASCII instruction, no history, tools or
summary. Its model-specific image reservations are not generalized to Unicode
text or arbitrary image sizes. Global `imageAdmissionQualified:false` and the
historical larger-image failures remain intact. Resource samples from live tests
are observations, not GPU admission/reclamation proof or a hard memory cap.

## Durable history, summary ownership and cleanup

`LoadContext` selects completed final assistant executions in assistant-completion
sequence order, **not user admission order**. Priority replacement is therefore
visible to an earlier-queued follow-up. Replacement prompt/attachment-ID
provenance refers to the original target; cancelled drafts are excluded. SQL
loads at most the newest six and oldest64 unsummarized eligible exchanges. Older
summary input may include up to four previously authorized results per exchange.
A separate bounded SQL selection retains the newest four authorized terminal tool
results (`completed` or `failed`, with canonical owner) from completed turns,
ordered by assistant completion sequence descending then tool-call ID. Its count
reports eligible results beyond the bounded selection without loading their text.
Recent exchange groups do not duplicate these blobs; current tool-call IDs also
deduplicate retained candidates. Sources are not deduplicated merely because text
or attachment IDs match: different instructions/actions remain different records.

The evidence block preserves recorded scope/tool/action/content-reference,
attachment ID, exact original arguments (including instruction), full-result and
arguments hashes, completion sequence, readable excerpt and status/coverage.
It tells the model to recall recorded observations rather than replace them with
later assistant paraphrases. Later human corrections remain distinct, not silently
folded into the observation. No original attachment table/store read, filename
authority, extraction or new analysis is involved. Omitted-result counts,
partial/failed extraction status, chunk byte coverage and excerpt truncation remain
explicit untrusted evidence. New analysis still needs scoped authorization.

### Original speaker attribution

An additional conversation-scoped query loads at most the earliest four completed
user/final-assistant message pairs outside the newest six, ordered by assistant
completion sequence. It reuses the existing completed count; there is no relevance
search, word/secret detector or unbounded anchoring. Current/cancelled turns and
other conversations are excluded. Message IDs, turn IDs, completion sequences,
SHA256 of original UTF-8 text and byte lengths identify each source. Current/recent
pairs deduplicate; distinct sources are not collapsed by equal text.

The final context combines `assistantGeneratedNotes`, exact `assistantReplies`,
and exact `userQuotations` in a single structured historical-data block, followed
by newer human corrections and the current request. The immutable summary text
is unchanged, but no longer presented as the only representation of older speaker
attribution. A quotation establishes **what that speaker wrote**, not factual
truth, compliance with a request, or application policy. In particular an earlier
assistant refusal remains recorded; recall must not fabricate successful storage.
No extra inference, summary component, deadline, publication or original attachment
access is introduced. The tested additive-user-block alternative did not resolve
Qwen recall and is not implemented.

This earliest-four slice may omit a later relevant fact/correction; recent history
and the lossy summary remain separate channels. Whole-pair omission under the
shared excerpt/total limits is explicit. Full original history stays in SQL/API.

Summaries use immutable version and final-assistant-message watermark in
`conversation_summaries`. Publication serializes under the conversation lock and
checks the exact contiguous eligible completion interval, prior version/boundary/
exchange count, selected model, current attempt and unclosed physical invocation
registration. IDs are deterministic by conversation/version. A different attempt
adopts the canonical winner; the same owner may replay identical output but may
not rewrite it. Budget metadata records the owner attempt, previous boundary,
number of exchanges, source limitation and generation accounting. Full original
messages/tool results remain in SQL and paginated API history.

Summary inference runs synchronously **inside the existing registered physical
ChatModel activity body** with a90-second child deadline. There is no detached
summary task and no new workflow I/O. Existing strict stop/replacement joining
therefore covers summary generation and every overlapping timed-out body.
Ordinary recovery still uses canonical output ownership separately from cleanup
acknowledgements. Summary input encodes previous memory and completed exchanges as untrusted JSON
data, rather than replaying historical user/assistant roles as active instructions.
Summary text contains only previously completed history, never the currently
incomplete turn.

Generation errors, blank/invalid/exhausted/oversized summaries or an older
exchange that cannot fit retain the prior summary (if any) plus bounded recent
context and explicitly report reduced coverage. An oversized first unsummarized
exchange cannot be skipped to advance a false gap-free watermark. Failed summary
**persistence propagates**; it is not reported as durable success. Lost ACK/retry
reads the committed winner instead of republishing changed memory.

## Schema8 and UI/API contract

Schema8 adds nullable, size-bounded `chat_model_results.context_metadata` and a
completed-context index. API and worker require schema8. Existing records may
have null context. No new public mutation endpoint is introduced.

`GET /conversations/{id}/messages` returns `context` for completed final model
answers; prompt/legacy records have null. `turn.completed` display payload also
contains it, but history is never reconstructed from display events.

- `context.coverage.recentExchanges`: selected recent complete exchanges.
- `omittedExchanges`: completed exchanges represented by neither the selected
  recent groups nor the included summary. Zero is **not** a full-fidelity claim.
- `summaryVersion`, `summaryBoundary`: included durable version/watermark, or0.
- `summaryFailure`: optional stable reason (`generation_failed`,
  `unusable_or_exhausted_summary`, `older_exchange_exceeds_budget`, or
  `summary_omitted_for_budget`).
- `sourceLimited`: true for bounded/lossy production history. UI must not say
  “full memory” even when every exchange has some lossy representation.
- `outputTruncated`: optional provider output-budget exhaustion flag.
- `evidence`: independent retention coverage: `eligible`, `included` (at most four
  source descriptors), `omitted`, `deduplicated`. Each descriptor retains `scope`,
  completion `sequence`, attachment ID, `argumentsHash`/`resultHash`, `excerptBytes`,
  `excerptTruncated`, original `sourceTruncated`, `sourceError` and optional
  `extractionStatus`. Hashes identify the original immutable values, not clipped
  excerpts. Full instruction/result text is deliberately absent from API metadata;
  the bounded model evidence message contains it. `scope.ContentRef` is the
  attachment checksum or canonical URL reference, not authorization to re-read.
  A retained source with zero excerpt bytes is explicitly clipped, not full recall.
- `quotations`: original-message coverage, with `eligible` older completed pairs,
  `included` (at most four `{user, assistant}` source-descriptor pairs), `omitted`,
  and `deduplicated`. Each descriptor has `sourceId` (message entry ID), `speaker`,
  `turnId`, completion `sequence`, `textHash` (SHA256 of original UTF-8 bytes), and
  `bytes`. No quotation text is stored in coverage. Pairs are whole, not clipped.
  These counts are separate from `omittedExchanges`, which continues to describe
  the summary/recent channels; neither can certify universal fidelity. The combined
  maximum tool/quotation descriptors fit the existing8192-byte metadata cap.
- `context.accounting`: `method`, `inputTokens`, `imageTokens`, `outputTokens`,
  `reserve`, `total`. These are admitted request bounds, **not measured tokens**;
  summary generation accounting is recorded separately on the summary version.

The Task11 UI can render a limited-memory/partial-source indication from these
fields while retaining the complete paginated transcript. Summary text itself
is internal and does not replace original messages.

## Verification and known boundary

- Named builder tests cover full budgets, recent selection, summary chains,
  retries/failure, tool pairs, provenance, chronology and partial evidence.
- `caffeinate -is scripts/test-web-integration.sh context` runs isolated real
  Postgres/Temporal, storage/API race checks, actual parser checks, context faults,
  cancellation/CAN/replacement, worker/store restart, existing hard-worker-death
  recovery, and document/image regression scenarios. Inference is injected except
  in the separately opted-in live test.
- `WEBCHAT_TASK10_LIVE=1` and an exclusive new
  `WEBCHAT_TASK10_EVIDENCE=/absolute/path.jsonl` enable `TestContextLiveModels`
  and the separate `TestContextLiveSummaryAttribution` regression. Select both with
  `WEBCHAT_TEST_CHAT_RUN='^TestContextLive(Models|SummaryAttribution)$'` and the same
  runner's `chat` mode. It uses actual Ollama and11 turns per exact model,
  authorized tiny TXT/image fixtures, four incremental summaries and restart.
  It records bounded logical requests (not image bytes), persisted coverage,
  transport, semantics, latency, source accounting and read-only sampled Ollama
  process RSS separately. The original semantic assertions are unchanged; do not
  weaken them to transport checks. No larger-image quality investigation is performed.

Both historical runs showed Qwen retaining the requested facts; the second had
three exhausted/blank summaries correctly rejected. Both historical Gemma memory
failures remain evidence, not retroactive passes. Continuation1 uses the same
models, caps, safe fixture and assertions, with separately retained observations
and summary-only no-reasoning configuration; the new live probe passes for both.
However, a subsequent Qwen run first refused to store the harmless user-supplied
phrase, then treated its own refusal as overriding that user statement. A general
summary attribution clarification did not fix the separately forced-refusal
regression (`TestContextLiveSummaryAttribution`) in continuation1: Qwen refused
final recall while Gemma passed. That probe explicitly injects only the initial assistant response;
subsequent summaries/final recall use the real registered model. It is **not** a
fallback or a replacement for the unchanged all-real main fixture. All failed
artifacts/assertions remain.

Continuation2 investigated exactly two predeclared representations, once per model.
Structured origins passed both; an independent additive user-quote block failed
Qwen and was rejected. Only structured origins was implemented. The subsequent
unchanged main and captured-refusal tests passed both exact models, with no output
exhaustion, on one actual qualification run. Qwen's refusal-case summary still
claimed no user facts; Gemma's main summary still misinterpreted the image. Correct
final recall alongside those failures demonstrates the separate-source path, NOT
that summaries became faithful. No rerun-until-green or assertion weakening.
The offline investigation test is separately opt-in and its failed candidate remains
in evidence; it is not a production acceptance gate to repeatedly sample.

Deterministic tests deliberately supply contradictory/forgetful summaries and
verify the actual final request retains correctly attributed evidence. Real
worker/store restart also succeeds with the test-owned original unavailable and
no reanalysis; denied content is never loaded. Budget/dedup/partial-source and
SQL ownership/order/count-limit cases are covered separately.

The latest-four tool rule and earliest-four quotation rule can omit relevant
sources or later corrections outside recent context, and total-budget pressure
can omit any optional evidence. No relevance search or unlimited-history promise
is implied. Source bounds are estimates, sampled host RSS is not GPU admission,
and a narrow passing semantic probe is not universal memory or image qualification.
