# Conversation-first Terminal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make prompts and responses immediately readable, with one collapsed work block per prompt and trustworthy semantic colors, faithfully translating the approved sketch into the actual terminal.

**Architecture:** Add a bounded conversation projection over existing durable records/live observations; keep strict outcome parsing and backend authority unchanged. Render semantic blocks with explicit source identities, adapting selection and anchors to projected blocks instead of assuming one rendered block per transcript record. Native chrome/composer use compact measured geometry.

**Tech Stack:** Existing pinned Go, Bubble Tea2, Bubbles2, Lip Gloss2, x/ansi; no new dependencies.

**Spec:** docs/superpowers/specs/2026-11-11-conversation-first.md (approved in chat; user requested execution without another design pause).

## Global Constraints
- Native Go/Bubble Tea/Bubbles/Lip Gloss only; no dependency upgrades, new runtime dependencies, workflow/activity/provider changes, replay fixture replacement or backend control API changes.
- Keep all existing published PRs draft/unmerged, preserve main and user's checkout/edit. Branch from draft22 into a new draft PR. No default-service restarts.
- Composer stays pinned at80x24 and120x40; normal header at most5 rows excluding exceptional warnings. Approval is never collapsed; tiny hidden decisions remain refused.
- Existing source/outcome/identity/log bounds and trust rules remain binding. Green response completion is not test verification; no success inference from prose.
- Actual terminal evidence and direct visual comparison are mandatory, not a substitute HTML implementation or test-only render acceptance.

## Review Focus
- Pages beginning mid-work must show partial ownership/counts, never assign preceding tools to the next prompt.
- Reused tool IDs, malicious headings and live/durable overlap must not merge prompts or grant outcome colors to untrusted body text.
- Partial/failed/stopped newest work must not recolor an unrelated historical response; errors stay visible while payloads remain collapsed.
- Expansion while browsing/resize/new data must retain correct durable selection and anchor; no unbounded expansion map.
- Long roots, no color, tiny terminals and unsent drafts must retain identity, role/outcome distinctions and safety without header consuming conversation.

### Task 1: Cohesive conversation projection, presentation and navigation

**Files:**
- Create `internal/tui/conversation.go` and focused conversation/render/navigation tests as appropriate.
- Modify `internal/tui/journal.go`, `navigation.go`, `styles.go`, `layout.go`, `model.go`, `history.go`, `render.go` only where needed for integration; preserve canonical parser logic.
- Update `internal/tui/README.md` and `docs/terminal-journal.md` for actual controls/limits.

**Interfaces:**
- Consumes `activities.TranscriptRecord`, `history.page`, `journalEntries`, canonical `durableToolText` / `toolOutcome`, current stream retirement and query authority; no new backend API.
- Produces semantic conversation blocks with stable source-record identities and bounded expansion state; `journalLines`/`rebuildJournal`/selection/anchor paths must share the SAME mapping (do not derive IDs from an old index assumption). Choose internal names/types together in this task and document them in report.

- [ ] Write content-level failing tests: user→large system evidence→tool-request assistant→tool result→final assistant collapses to readable conversation +one work summary; raw payload/args/RecordN hidden; user/assistant roles distinct; completed response label does not imply verified command. Test multiple prompts, partial page before first user, reused call IDs, zero work, huge inputs and bounded expansion eviction.
- [ ] Write failing tests for visible validated command failures/unknowns and request failures while collapsed; raw read-file text containing success/failure/role strings cannot control semantic coloring. Historic response must not inherit unrelated current failure.
- [ ] Write failing tests for inline work expansion via discoverable non-typing control, raw detail/log reachability, frozen browsing plus new update and80→120→80 resize, selection after group collapse, latest/older/source switch reset. Retain previous regression guarantees with new geometry.
- [ ] Write failing terminal layout/style tests at80x24/120x40/tiny/NO_COLOR: compact normal header<=5 rows, distinct user and assistant heading/markers, amber unknown and red failure, neutral body, separated pinned multiline composer. No queue0/Submitstarted chatter, raw context or genericRecordN in normal view.
- [ ] Run focused tests with `mise exec -- go test -p 1 ./internal/tui -run 'Conversation|Group|Compact|Role' -count=1` and record meaningful red failures.
- [ ] Implement bounded projection and semantic styling; adapt navigation and history mapping coherently; preserve detail/log parser and safety. Default collapse all intermediate work. Use Ctrl+T for explicit work toggle (selected group, otherwise latest visible group) and visible hint; preserve typing/Enter/Alt+Enter/Esc/Ctrl+C and existing detail/log commands. Selection must have a clear visible target.
- [ ] Implement compact aligned header/status and separated composer matching the approved sketch's structure. Read the actual HTML reference supplied in design brief, not just this prose. No browser implementation.
- [ ] Run covering TUI tests, update legacy presentation assertions while preserving what they prove, and serialized full build/vet/unit/race/replay. Commit only scoped source/tests/docs; ignored report with interfaces, exact controls, red/green evidence, commits and remaining limitations.
- [ ] Controller: independent spec/quality review, scoped fixes/review as needed; then fresh root gate. Capture actual PTY representative conversation/expanded/failure/working/approval views at80/120 columns and verify hidden noise and pinned drafts via keystrokes.
- [ ] Controller: separate visual evaluation explicitly compares real terminal captures to sketch and three approved readability requirements. Address blocking differences; preserve honest reference-profile rendering labels. Publish draft23 based22, wait both CI green, audit draft chain/main/primary edit; detach implementation worktree for manual checkout.

## Preflight
One cohesive task because projection identities, navigation geometry and role styling share the same render mapping. Self-consistency: tests and implementation both require one bounded per-prompt work block, canonical outcome authority, and measured pinned layout. No task-pair boundary exists. Prior one-record-per-render-part assumption is intentionally replaced, not retained as a conflicting requirement.
