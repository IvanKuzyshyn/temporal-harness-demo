# Bounded context and durable memory (Task10)

**Continuation1 adds independently retained canonical evidence; overall semantic
acceptance remains BLOCKED.** The latest original both-model document/image-memory
probe passes unchanged, but a repeat exposed Qwen overriding a user-stated fact
with an assistant refusal, and a separate captured-refusal regression still fails.
A passing narrow run is not universal full-fidelity memory qualification. The two failed runs remain recorded:
`task-10-live-context.jsonl` and `task-10-live-context-data.jsonl` in the controller's
dispatch directory. Gemma had promoted a contradictory assistant interpretation
into its summary after the canonical image result left recent history. The fix
keeps bounded approved observations separately attributed alongside lossy memory.
“Canonical” means immutable recorded output, **not guaranteed factual truth** of
image/document inference. No semantic waiver, fallback, cap increase or image
profile expansion. See `task-10-report.md` and the new exclusive
`task-10-live-canonical-evidence*.jsonl` artifacts.

## Production assembly and admission

`internal/context.Builder.Build(ctx, conversationID, turnID, budget)` loads a
bounded SQL snapshot and optionally advances durable older memory. Its mandatory
request and invocation attempt are supplied by `ChatModel`. `Assemble` is the
shared final admission path for ordinary/web chat, summaries, document chunks
and the **separate Task9 image profile branch**. It is not a detached estimator.

Default limits are context16,384, estimated text input12,000, output2,000,
reserve2,384, at most six completed recent exchanges, summary bound1,500,
and readable evidence excerpts4,000 UTF-8 bytes shared across current tool groups
and separately retained results (and any legacy injected recent-result seam). All sublimits remain subordinate to the final total. Mandatory
system instructions, current prompt, replacement provenance, schemas and complete
current assistant/tool groups are checked before optional history or summary
inference. Oversized mandatory input fails; it is never silently clipped. Recent
exchanges are evicted whole, oldest first; assistant/tool pairs are indivisible.
Current groups consume the excerpt allowance first. Up to four independently
retained observations are admitted next, then optional summary/recent exchanges.
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
regression (`TestContextLiveSummaryAttribution`): Qwen still refuses final recall;
Gemma passes. That probe explicitly injects only the initial assistant response;
subsequent summaries/final recall use the real registered model. It is **not** a
fallback or a replacement for the unchanged all-real main fixture. All failed
artifacts/assertions remain. Do not mark overall memory acceptance complete.

Deterministic tests deliberately supply contradictory/forgetful summaries and
verify the actual final request retains correctly attributed evidence. Real
worker/store restart also succeeds with the test-owned original unavailable and
no reanalysis; denied content is never loaded. Budget/dedup/partial-source and
SQL ownership/order/count-limit cases are covered separately.

The latest-four rule can omit an older relevant source, and total-budget pressure
can omit any optional evidence. No relevance search or unlimited-history promise
is implied. Source bounds are estimates, sampled host RSS is not GPU admission,
and a narrow passing semantic probe is not universal memory or image qualification.
