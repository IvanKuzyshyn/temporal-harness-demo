# Local Project Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the harness a grounded assistant for its launch directory with an informative, durable, interactive terminal journal.

**Architecture:** Keep the existing Temporal orchestration, local Ollama adapter and approval policies. Add authoritative session configuration, fresh bounded activity-side project evidence, a safe local paged transcript reader, and a chronological TUI journal with live-event reconciliation. Version workflow scheduling changes so the existing replay fixtures remain valid.

**Tech Stack:** Go 1.27.3; Temporal SDK 1.50.0/API 1.64.2/CLI 1.10.0; Ollama 0.40.15; Bubble Tea 2.1.1, Bubbles 2.2.1, Lip Gloss 2.1.0.

**Spec:** `docs/superpowers/specs/2026-11-10-project-assistant.md`

## Global Constraints
- New linear draft PRs based on #18; never merge, mark ready, enable auto-merge, push main or rewrite existing PRs.
- Preserve user's primary checkout/sample edit. Work in `/Users/ivankuzyshyn/projects/wt-harness-improvements` and scoped task worktrees.
- Local workers/filesystem only; no cloud runtime, version upgrades or sandbox claims.
- Filesystem/model/tool I/O stays outside deterministic workflow code; unsafe actions retain approval and no automatic retries.
- Existing captured histories must replay unchanged; use GetVersion for changed workflow command sequences.
- Bounded context/history/output with terminal sanitation. Actual session configuration, not client flags, is authoritative on reconnect.
- Normal startup uses launch directory; fixture is explicit opt-in. No fake progress percentage or unexecuted-test success claims.
- Each draft requires build/vet/full tests/race, independent review and green CI before next draft. Serialize expensive tests with `-p 1`.

## Review Focus
- Launching a built binary from an unrelated directory or through mise must inspect that directory, not the harness/sample (Task 1).
- Reattaching from a new workdir/model must expose the original session configuration and mismatch, including unavailable legacy state (Task 1).
- README instructions, missing manifests, large/generated trees and changes after previous answers must not create fabricated/stale evidence (Task 2).
- Delayed reads/reconnect/same tool IDs across logical runs must not splice wrong-session content or duplicate outcomes (Task 3).
- Short terminals and incoming output while scrolled up must not hide the prompt/approval controls or jump history (Task 4).

---

### Task 1: Launch-directory workspace and authoritative session metadata (PR 19)
**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`, `mise.toml`, `cmd/harness/{interactive,run}.go` and CLI tests; `internal/workflows/{contracts,session}.go` and metadata tests; `internal/tui/{model,render}.go` and tests; README/operations docs.
**Interfaces:** `Config.Workdir` is an absolute path; add `workflows.SessionInfo` with `Model, Workdir, DataDir, Scenario string` JSON fields and `State.Session *SessionInfo` (`json:"session,omitempty"`). It represents original workflow input, recomputed after restoring Carry, without I/O. CLI workdir flag overrides env, env overrides CWD. TUI labels unknown legacy configuration explicitly and compares client selection against authoritative metadata.
- [ ] Write failing tests: `TestWorkdirDefaultsToLaunchDirectory`, env/flag precedence, execution from temp directory through mise (no hardcoded config_root), `TestSessionInfoSurvivesRestore`, and reconnect mismatch/legacy UI tests. Assert original root/model remain unchanged and visible.
- [ ] Run targeted config/CLI/workflow/TUI tests and record expected red assertions.
- [ ] Implement CWD fallback and remove mise's fixture default. Add explicit CLI `--workdir` to run/tui/demo with consistent absolute resolution. Publish session metadata without scheduling new activities. Ensure normal mode does not imply scripted demo. Keep demo usage on explicit disposable fixture; do not write/copy automatically.
- [ ] Update tests/docs for root precedence and fixed session config. Run full serialized build/vet/unit/race and unchanged replay.
- [ ] Commit scoped changes; independent review; orchestrator verifies/publishes draft base `stack/18-replay-docs`, waits green CI.

### Task 2: Fresh, evidence-grounded project analysis (PR 20)
**Files:** Create `internal/activities/project_evidence.go` and tests, `internal/activities/agent_prompt.go` and tests. Modify `internal/activities/{context,model}.go`, `internal/workflows/{contracts,toolexec,loop}.go`, their tests, and grounding docs.
**Interfaces:** Extend `GatherInput` with stable `InputID string`. Version fresh per-logical-run gathering with `workflow.GetVersion`; legacy histories keep original scheduling. `LLMInput` may add `Workdir string` for trusted scope labeling. Add pure `agentMessages(messages []llm.Message, workdir string) []llm.Message`: prepends parent-agent policy, treats all project context as quoted data, removes superseded context from requests while leaving durable transcript untouched. Explorer instruction/policy must remain read-only and not be silently replaced.
- [ ] Write failing tests using real temporary projects and a capturing fake model: two different roots, relevant manifest + implementation evidence for 'explain this project', no unconditional calculator content, modified source/README in later run, denied/unreadable paths, malicious README instructions as data, and no source evidence falsely presented as executed tests.
- [ ] Record red targeted tests. Add a fixture with non-Go code so discovery isn't hardcoded to calculator or this repository.
- [ ] Implement bounded automatic project discovery/evidence in activities: cap visited entries at 4096, source/manifest reads at 8 files and 8 KiB each, aggregate evidence at 32 KiB. Skip hidden entries and dependency/build directories (`node_modules`, `vendor`, `dist`, `build`, `target`), never follow escaping symlinks, report truncation/read failures. Prefer recognized manifests and likely source entry points, with bounded general-source fallback. Report paths and observed contents explicitly. Never run code for discovery.
- [ ] Implement trusted coding-agent instruction: use tools to inspect relevant source when evidence is insufficient, answer user intent rather than fixture script, cite inspected files, report uncertainty/unavailable access, never claim commands ran based on prose. Refresh per logical run with stable retry keys. Preserve old history replay via version gating; do not rewrite captured histories.
- [ ] Verify capturing-model tests, workflow version branches, full suites/races, actual local Ollama ordinary (not /demo) analysis of temporary unrelated projects. Document exact observed model behavior and limits.
- [ ] Commit/review; publish draft base PR19 only after its CI is green; wait this CI green.

### Task 3: Durable paged history and journal reconciliation (PR 21)
**Files:** Create `internal/activities/transcript_reader.go` plus tests (or a focused shared artifact package if needed), `internal/tui/history.go`, `internal/tui/journal.go` plus tests; modify transcript reader internals, TUI model/io/render, relevant docs.
**Interfaces:** Export read-only `activities.ReadTranscriptPage(ctx context.Context, dataDir, workflowID string, before int, limit int) (TranscriptPage,error)`. `TranscriptPage` contains `Entries []TranscriptRecord`, `Before int`, `HasMore bool`, `Total int`; `TranscriptRecord` contains stable `ID string`, `Message llm.Message`. `before=0` means newest page, maximum page size 64; older pages use returned Before cursor. Reader derives exact artifact ref from workflowID, limits bytes to existing 32 MiB, rejects symlink escape through data/artifacts, and validates cancellation/JSON. No workflow I/O. TUI requests pages only in tea.Cmd with session epoch and query-generation checks; authoritative session DataDir used after Query.
- [ ] Write failing tests for durable conversation after reconnect and snapshot, same tool IDs in different logical runs, live→persisted dedup by message identity, page boundaries/new appends, empty/malformed/oversized/symlinked artifacts, stale asynchronous responses after session switch, missing optional JSONL with intact transcript, and transcript errors shown without discarding Query status.
- [ ] Record red tests. Implement reader and ID-based chronological journal. Preserve complete persisted messages and paired tool outcomes instead of clearing them on every snapshot. Bound rendered text/loaded records; allow explicit older-page loading rather than promising infinite memory. Tool details retain full bounded result text and log refs rather than two lines.
- [ ] Merge live events onto persisted entries without duplicated final answers. Query remains authoritative for status/approval, not a substitute for conversation history. Preserve meaningful parent/child distinctions; child completion summary is durable, live child updates may be lossy and labeled accordingly.
- [ ] Verify existing reconnect/retry tests, new history tests and full build/vet/unit/race. Review/publish draft base PR20 and wait green CI.

### Task 4: Terminal journal UX, pinned prompt, progress and final qualification (PR 22)
**Files:** Create `internal/tui/{layout,progress,styles}.go` and focused tests; modify `model.go`, `render.go`, `commands.go`, history/journal integration and approval sizing. Update README, operations/testing, stack inventory; add reproducible isolated smoke tooling/tests where justified.
**Interfaces:** Consume `State.Session` and journal entries from prior tasks. Keep existing Backend control methods and exact decision binding. Rendering remains pure; all file/network work is tea.Cmd. New UI state owns selected journal item, expansion, explicit follow-live/unread state and scroll anchor.
- [ ] Write failing 80x24/120x40/narrow/tiny render tests asserting visible prompt and bounded output. Add event tests for scroll preservation, unread counts, jump-latest, mouse wheel, detail expansion, navigation vs text input, reduced/no color status meaning, and approval controls visible or explicit resize warning at tiny sizes.
- [ ] Record red tests. Implement chronological journal with semantic outcome colors + text. Keep selected tools expandable with scrollable detailed output. Add discoverable keys without breaking typing/Enter/Alt+Enter/Esc/Ctrl+C/approval. Prompt fixed at bottom, viewport measured from remaining terminal rows; no fixed height chrome that clips prompt.
- [ ] Show authoritative project/model/mode; real workflow phase, active tools and children, elapsed running phase and last observed heartbeat age/unknown. Animate activity for active work, not only query polling. No percentage for unknown-total generation. Mark read evidence separately from execution verification.
- [ ] Implement mouse/keyboard history scrolling and stable anchors while updates arrive; End jumps latest and clears unread count. Distinguish paging older data from truncated/unavailable artifacts.
- [ ] Run full build/vet/unit/race/replay and isolated live normal/race durability. Run actual real-model ordinary repository analysis and mock guided demo on temporary projects; actual PTY test pinned prompt, scroll/unread/details, approval and reconnect. Never disturb default services. Independent whole-stack review with scoped fix/re-review as needed.
- [ ] Publish draft base PR21, wait both CI checks green, verify all old/new PRs draft/unmerged/no-auto-merge and main unchanged. Update final inventory and manual instructions. Leave final new branch free for user's manual checkout by detaching implementation worktree after completing gates.

## Execution authorization and decisions
The user explicitly requested immediate autonomous execution and will manually review the final result. Do not pause for another plan approval. Sequential task agents and independent reviewers will operate in isolated worktrees; only the orchestrator publishes the authorized draft stack. Existing browser mockup is referenced for visual intent, not shipped as production UI.
