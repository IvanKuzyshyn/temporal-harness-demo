# Chat simplification implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: executing-plans. Sole implementer; no helpers/reviewers. Scope already approved; continuous execution.

**Goal:** Reference-matched compact chat, Qwen-only enabled catalog and one attachment per prompt.
**Architecture:** Preserve durable workflow/storage contracts. Narrow admissions using shared catalog/attachment limit, incrementally constrain new SQL writes, simplify presentation without removing consent or durable state.
**Tech Stack:** Go/Postgres/Temporal, Python lifecycle, Next/React/TypeScript, Vitest, agent-browser.
**Spec:** `docs/superpowers/specs/2026-11-13-chat-simplification-design.md`

## Global constraints
- Only supplied worktree/branch; no Task14 WIP access, no active-user-app changes, no push/PR/merge.
- Exact Qwen digest/runtime/image bounds and false global qualification unchanged; no Gemma inference/download, zero local inference planned.
- One attachment per message/replacement; 1,048,576 bytes/type/ownership/checksum/approval fences unchanged.
- Retain data/evidence; unique test resources; serialized Go; explicit deadlines.

## Review focus
- Legacy Gemma config/history must not force installation or silently switch models.
- Restored old multi-file selections/pending submissions cannot bypass admission or silently lose files.
- Rapid second file selection must not start a parallel upload; navigation completion belongs to originating conversation.
- Late HTTP ACK cannot clear newer draft/request; retries keep identity.
- Removed diagnostics must not hide failed responses/required approvals or restore unsafe media.

## Source map / interfaces
- `internal/chat/contracts.go`: `ValidModel(string) bool`, `CommandInput.Digest()` and shared enabled catalog / max attachment bound.
- `internal/api/server.go`: GET models; `internal/storage/repository.go`: `AcceptCommand`, `validateMessage`, `AppendMessage`; new migration for new-write model and ordinal restrictions.
- `internal/llm/ollama/image_profile.go`: exact model profiles; `scripts/dev/lifecycle.py`: `Driver.models`, existing-config environment normalization; `infra/runtime-pins.json` and current operational docs.
- `web/lib/attachment-batches.ts`: `uploadFiles`, conversation-owned pending lock; `web/components/attachment-picker.tsx`: picker/drop and preview; `chat-shell.tsx`: draft restoration/reuse/send/replacement and idempotency.
- `web/components/message-list.tsx`, `approval-card.tsx`, `tool-details.tsx`, `web/app/globals.css`: remove debug render paths, retain essential errors/consent, reference layout.

## Task 1: backend/config boundaries
- [ ] Add RED cases for disabled model catalog/admission/profile, two attachment command/replacement/storage writes, lifecycle Qwen-only readiness even with Gemma absent and old config.
- [ ] Run focused Go/Python tests; preserve original RED logs.
- [ ] Implement catalog/max1 validation and additive SQL new-write constraints (legacy rows retained), lifecycle/pins/docs narrowing. Preserve exact Qwen gates and generic provider routing.
- [ ] Run GREEN focused tests, actual isolated API/storage integration (including idempotent retries, multiple standalone blobs and disabled model), full Go unit/build/vet/race as feasible.
- [ ] Commit backend/config and tests separately.

## Task 2: frontend boundaries and reference presentation
- [ ] Add RED tests for multi-drop/rapid second selection, stale selection/pending restoration, submit/replacement max1, navigation ownership; absence of rendered diagnostics with visible approvals/errors. Preserve existing retry/late-ACK regression coverage.
- [ ] Run `mise exec -- pnpm --dir web test`; expected new behavioral failures, retain logs.
- [ ] Implement max1 at picker/upload manager/reuse/restore/submit boundaries; keep invalid old selections removable. Match inspected reference with local system fonts/icons, functional sidebar/new prompt composer, compact necessary controls; remove diagnostic components entirely.
- [ ] Run full frontend tests/typecheck and clean production build; expected exit0.
- [ ] Inspect browser desktop/mobile empty/populated/approval/error/upload/navigation using deterministic owned fixtures, save screenshots; no local inference.
- [ ] Commit frontend/tests separately.

## Task 3: final verification/report
- [ ] Self-review scoped diff (not independent review), run final checks, verify active resources unchanged and owned runtimes stopped; retain all data/logs.
- [ ] Write canonical report with exact commands/logs, reference/app screenshots, commit IDs, omissions, zero-call ledger, transition and isolated reviewer startup instructions.
- [ ] Checkpoint before context threshold and continue; controller owns independent review/push/PR. Old Task14 remains incomplete.

## Progress / rulings
- Recon complete: model/provider/reasoning verified openai/gpt-6-astra high; base and branch correct. Local compaction enabled with reserve108800/keepRecent20000 verified. Existing worktree isolation verified.
- Frontend baseline: 57/57 tests pass using pinned `mise exec -- pnpm`. Initial unpinned corepack invocation failed to parse pnpm12 lockfile; no files changed; original setup failure retained.
- Ruling: retain legacy SQL rows with NOT VALID new constraints rather than reset; disabled stored models must fail closed. This avoids user-data disruption; old histories are not promised runnable.
- Ruling: controller-specified evidence/report workspace overrides skill scratch deletion. No evidence removed; no independent reviewer dispatched.
