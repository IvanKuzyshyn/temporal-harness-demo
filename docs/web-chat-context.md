# Bounded context and durable memory (Task10)

**Acceptance remains blocked on live Gemma incremental-memory quality.** The
implementation and deterministic/real-infrastructure gates are separate from
semantic qualification. Both exact models transported the safe conversation,
document and image calls successfully. The initial Gemma run lost the secret
phrase/image attribution. Encoding historical roles as data improved retention,
but the follow-up run still misrepresented the image result in memory. Qwen
recalled the facts, with three exhausted summary attempts safely rejected in the
follow-up run. No fallback or model change is authorized. See canonical
`task-10-report.md`, `task-10-live-context.jsonl` and
`task-10-live-context-data.jsonl` in the controller's Task10 dispatch directory.

## Production assembly and admission

`internal/context.Builder.Build(ctx, conversationID, turnID, budget)` loads a
bounded SQL snapshot and optionally advances durable older memory. Its mandatory
request and invocation attempt are supplied by `ChatModel`. `Assemble` is the
shared final admission path for ordinary/web chat, summaries, document chunks
and the **separate Task9 image profile branch**. It is not a detached estimator.

Default limits are context16,384, estimated text input12,000, output2,000,
reserve2,384, at most six completed recent exchanges, summary bound1,500,
and readable evidence excerpts4,000 UTF-8 bytes shared across current and recent
tool results. All sublimits remain subordinate to the final total. Mandatory
system instructions, current prompt, replacement provenance, schemas and complete
current assistant/tool groups are checked before optional history or summary
inference. Oversized mandatory input fails; it is never silently clipped. Recent
exchanges are evicted whole, oldest first; assistant/tool pairs are indivisible.
Older summary precedes recent completed corrections. Output exhaustion uses both
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
requests explicitly set `truncate:false`, `shift:false`, `runner:mlx`.

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
loads at most the newest six and oldest64 unsummarized eligible exchanges, plus
at most four previously authorized persisted results per exchange. It never
opens original attachments or reruns extraction/analysis. Omitted-result counts,
partial/failed extraction status, chunk byte coverage, summary exhaustion and
excerpt truncation remain explicit untrusted evidence. New analysis still needs
a new scoped authorization.

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
  `WEBCHAT_TASK10_EVIDENCE=/absolute/path.jsonl` enable `TestContextLiveModels`.
  Select it with `WEBCHAT_TEST_CHAT_RUN='^TestContextLiveModels$'` and the same
  runner's `chat` mode. It uses actual Ollama and11 turns per exact model,
  authorized tiny TXT/image fixtures, four incremental summaries and restart.
  It records transport, semantics, latency, source accounting and read-only
  sampled Ollama process RSS separately. The Gemma memory assertion intentionally
  remains failing until the loss is addressed/accepted; do not weaken it to a
  transport check. No larger-image quality investigation is performed.

Both live runs showed Qwen retaining all three requested facts. The first Gemma
run lost the secret and image attribution. After the transcript-as-data correction,
Gemma retained the secret but summarized the image as “no French text was found,”
contradicting the canonical image result “Hello world.” This distinction between
canonical evidence and the assistant's interpretation remains a semantic blocker.
The follow-up Qwen run had three exhausted/blank summaries (not persisted) and
one usable canonical summary; final memory succeeded with explicit limited
coverage. Bounds and persistence succeeded, but lossiness alone is not evidence
that long-conversation memory is usable. Follow-up needs an
explicit decision: improve the same-model incremental-memory protocol with
regression evidence, or explicitly accept that reduced semantic boundary. No
such quality waiver is assumed here.
