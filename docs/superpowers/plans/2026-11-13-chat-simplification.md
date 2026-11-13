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
- [x] Add RED cases for disabled model catalog/admission/profile, two attachment command/replacement/storage writes, lifecycle Qwen-only readiness even with Gemma absent and old config.
- [x] Run focused Go/Python tests; preserve original RED logs.
- [x] Implement catalog/max1 validation and additive SQL new-write constraints (legacy rows retained), lifecycle/pins/docs narrowing. Preserve exact Qwen gates and generic provider routing.
- [x] Run GREEN focused tests, actual isolated API/storage integration (including idempotent retries, multiple standalone blobs and disabled model), full Go unit/build/vet/race as feasible.
- [x] Commit backend/config and tests separately.

## Task 2: frontend boundaries and reference presentation
- [x] Add RED tests for multi-drop/rapid second selection, stale selection/pending restoration, submit/replacement max1, navigation ownership; absence of rendered diagnostics with visible approvals/errors. Preserve existing retry/late-ACK regression coverage.
- [x] Run `mise exec -- pnpm --dir web test`; expected new behavioral failures, retain logs.
- [x] Implement max1 at picker/upload manager/reuse/restore/submit boundaries; keep invalid old selections removable. Match inspected reference with local system fonts/icons, functional sidebar/new prompt composer, compact necessary controls; remove diagnostic components entirely.
- [x] Run full frontend tests/typecheck and clean production build; expected exit0.
- [x] Inspect browser desktop/mobile empty/populated/approval/error/upload/navigation using deterministic owned fixtures, save screenshots; no local inference.
- [x] Commit frontend/tests separately.

## Task 3: final verification/report
- [x] Self-review scoped diff (not independent review), run final checks, verify active resources unchanged and owned runtimes stopped; retain all data/logs.
- [x] Write canonical report with exact commands/logs, reference/app screenshots, commit IDs, omissions, zero-call ledger, transition and isolated reviewer startup instructions.
- [x] Checkpoint before context threshold and continue; controller owns independent review/push/PR. Old Task14 remains incomplete.

## Progress / rulings
- Recon complete: model/provider/reasoning verified openai/gpt-6-astra high; base and branch correct. Local compaction enabled with reserve108800/keepRecent20000 verified. Existing worktree isolation verified.
- Frontend baseline: 57/57 tests pass using pinned `mise exec -- pnpm`. Initial unpinned corepack invocation failed to parse pnpm12 lockfile; no files changed; original setup failure retained.
- Ruling: retain legacy SQL rows with NOT VALID new constraints rather than reset; disabled stored models must fail closed. This avoids user-data disruption; old histories are not promised runnable.
- Ruling: controller-specified evidence/report workspace overrides skill scratch deletion. No manual evidence cleanup; no independent reviewer dispatched. Playwright automatically reused one early failure trace directory before archiving was added; original logs remain and this evidence limitation is disclosed in the report.
- Task 1: complete, commit `5cd5345`. Serialized Go unit/build/vet pass; 7 touched race packages pass; lifecycle20 pass; real API/storage/dispatcher87 pass plus scoped real SVG download1 pass. Fresh schema9 migration/new-write SQL and legacy disabled-model reads/admission tested. Historical upgrade modes not claimed.
- Task 2: complete, commit `14360e2`. Vitest60/60, typecheck, clean webpack production build, Playwright11/11. Actual agent-browser desktop/mobile empty/populated/approval/error/upload/navigation/queued scenarios and served-CSS hash equality recorded. No local inference. First-prompt413 and mobile sidebar focus regressions received additional RED→GREEN coverage.
- Task 3: complete as implementer handoff. Self-review performed; current API/storage/UI docs corrected. Controller owns independent reviews, publication and CI. Canonical report is `simplification-report.md` in controller evidence workspace; all owned runtimes stopped, resources retained. User's original API/Next/Temporal/Ollama/container identities remain running unchanged.
- Ruling: reuse only this session's retained stopped integration project with a fresh database per invocation after Docker address-pool exhaustion; never prune shared/retained networks. Reviewer helper refuses an already-running owned project. New user-home startup may require operator capacity remediation, not unauthorized cleanup.
- Verification limits: six real private Postgres/Temporal tests used deterministic providers; visual saved states were synthetic and new commands queued without a dispatcher. Zero physical Ollama calls. Old Task14/both-model/four-file qualification remains incomplete and needs separate adaptation. Existing Qwen semantic waivers/global image qualification remain unchanged.
