# Terminal UI

```go
model := tui.New(backend, tui.Options{
    SessionID: "session-1",
    Model:     "mock",
    DataDir:   dataDir,
    Input:     input,
}) // *tui.Model implements charm.land/bubbletea/v2.Model
defer model.Close() // Cancel outstanding commands when the caller exits.
```

The caller runs the model with Bubble Tea. This package does not start a terminal,
Temporal client, workers, or external processes itself. `Backend` defines the six
session operations (`Submit`, `Query`, `Stop`, `Continue`, `Decide`, `Sessions`).
All backend calls, transcript/log-page reads, and event-file reads run in commands with session-generation
checks on results. Backend implementations should honor context cancellation;
calls have a ten-second timeout. Ctrl+C cancels the session context.

## Controls

- Enter sends `WhenBusy: "followUp"`; Alt+Enter or `/steer <text>` sends `"steer"`.
- Shift+Enter inserts a newline; Ctrl+J is the fallback for terminals that cannot
  distinguish Shift+Enter. Pasted multiline text is retained.
- Esc requests Stop; Ctrl+C quits (does not implicitly stop the workflow).
- Tab switches journal/pattern log; PgUp/PgDn or mouse wheel browse the selected
  panel without following updates. End follows latest and clears unseen updates.
- Ctrl+Up/Down selects a durable record; Ctrl+O expands/collapses it. Ordinary
  typing and Up/Down still edit the pinned composer.
- Ctrl+L or `/log` opens a selected command's validated log; Ctrl+Left/Right or
  `/logprev` / `/lognext` page bytes beyond the model tail. `/metadata` shows
  sanitized original session values as scrollable rows.
- Ctrl+T collapses/expands tool details in the chronological journal.
- `/older` loads the preceding 64-record window; `/latest` retries/returns to the
  newest window. `/detail <record number>` opens scrollable captured record
  details; `/journal` returns to the window. PgUp/PgDn also scroll record details.
- `/continue`, `/new [id]`, `/sessions [id]`, `/help` are local commands.
  `/sessions <id>` reconnects to that session. `/new` generates a crypto-random ID.
- `/explore ...` and `/run ...` are submitted unchanged for workflow handling.
- `/demo` sends the recognized guided prompt; workflow routing applies per logical
  run, including an already-open session.
  `Options.Demo` only offers the demo banner. Startup never submits by default;
  `Options.Autoplay: true` explicitly submits the demo once after starting the tail.

## Reconnect and rendering

The first `events.Tail` completes before a snapshot query is dispatched. Further
tails run while that query is pending and are buffered with `events.NewReconnect`.
Only events newer than the snapshot's `LastSeq` are replayed. Sequence gaps,
truncation, and buffer exhaustion trigger a fresh snapshot. Epoch and query
identifiers reject obsolete asynchronous results, including clipboard reads.
Gap retries retain the bounded unprocessed suffix, including after a failed
Resolve. Overflow rewinds the tail. Session checkpoints (up to 32) use offset zero:
switching clears displayed state and rereads the file because a last-read offset
alone cannot reconstruct output newer than the snapshot. This trades extra local
reads for correctness, including switches during an initial query.

A 150ms polling tail and independent two-second snapshot refresh ensure the final
answer/status do not depend on a final stream event. A bounded replay cache keeps
newer deltas visible when an in-progress snapshot lags the live stream, without
repeating tool/pattern logs. Query owns status/approval; completed conversation
comes from durable transcript pages. Query answers are explicitly labeled
fallbacks only until a nonempty history window is available. Original raw session
DataDir is used for history, never a guessed client directory. Missing optional
JSONL cannot erase the persisted conversation.

The UI retains one contiguous page of up to 64 complete durable records (from a
32MiB-bounded artifact), plus 128 entries each for local notices, pattern log, and
command events (4KiB per entry), 128 live segments (64KiB assistant / 4KiB tool),
and 512 compact replay events. A separate FIFO of up to 512 completed MessageRef
hashes retires matching live entries across page eviction and snapshot replay;
this bounded replay horizon resets on session/original-directory changes.
Workflow and parent keys are SHA-256 identities separate from sanitized display
labels, including in pending reconnect events. The journal overview limits durable record text
to 4KiB each; selected detail permits 504KiB. Aggregate journal viewport text is
bounded to 512KiB, with explicit truncation labels. Pending reconnect buffers use the events package's 4096-event cap,
with a same-sized TUI projection buffer; the events-package copy validates only
sequence numbers. Prompt input has a hard aggregate 16,384-rune
bound (at most 64KiB UTF-8), independent of display width; all keyboard and paste
insertions share sanitization and the retained-rune budget.
Display payloads are compacted before buffering, and C0/C1/escape and Unicode
format controls are removed from untrusted display text. Rendering is clipped to
the terminal dimensions; extremely small terminals necessarily hide some panes.

## Approval and limits

- `modal.go` displays authoritative `PendingApproval`. `a` approves; `d` opens
  denial-reason entry; Enter submits a nonempty reason. PgUp/PgDn scroll the
  bounded preview/arguments. Decisions retain the exact request/hash binding;
  no approval or rejection is sent implicitly.
- Approval layout is tested at 80×24 and 40×14. Below 40×12, a RESIZE warning
  replaces it and both approval and denial dispatch are refused. Esc/Ctrl+C
  remain available; denial reason survives resizing. The normal composer is
  absent during approval, not misleadingly editable.
- Tool results and log references are available in the scrollable journal/detail
  view, not limited to the old two-line Commands pane. This is not a terminal emulator.
- Older browsing replaces the window and suppresses live suffixes rather than
  retaining unbounded history. Snapshots do not replace an older window. Tool
  pairing never crosses a page boundary or reuses another assistant's call ID.
- Original write diffs may not exist in old artifacts. Requested arguments and
  captured results are not represented as reconstructed historical diffs.
- [Durable history](../../docs/durable-history.md) documents the exact reader,
  safety/cancellation bounds, reconciliation rules, controls, and limitations.
- `layout.go` measures chrome before allocating the journal above the fixed
  composer. `navigation.go` freezes the bounded window while browsing and retains
  durable record/within-record row anchors across resize. `progress.go` shows
  authoritative phase/mode/timing/counts and observed heartbeat age, never Query
  polling as agent activity or prose as execution verification.
- `log.go` extracts references only from canonical validated outcomes. The reader
  pins the exact workflow's log directory and reads one 16 KiB byte page, capped
  at 64 MiB with an explicit limit. No shell or arbitrary path opener is exposed.
- [Native terminal behavior](../../docs/terminal-journal.md) documents controls,
  display bounds, frozen-window notification semantics and controller-owned PTY
  qualification. Unit tests are not actual terminal automation.
- Shift+Enter/Alt+Enter recognition depends on terminal support; slash steering
  and Ctrl+J provide alternatives.

Tests use fake backends and temporary event stores without a terminal:

```sh
mise exec -- go test ./internal/tui
mise exec -- go test -race ./internal/tui
```
