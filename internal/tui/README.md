# Basic TUI

```go
model := tui.New(backend, tui.Options{
    SessionID: "session-1",
    Model:     "mock",
    DataDir:   dataDir,
    Input:     input,
}) // *tui.Model implements charm.land/bubbletea/v2.Model
```

The caller runs the model with Bubble Tea. This package does not start a terminal,
Temporal client, workers, or external processes itself. `Backend` defines the six
session operations (`Submit`, `Query`, `Stop`, `Continue`, `Decide`, `Sessions`).
All backend calls and event-file reads run in commands with session-generation
checks on results. Backend implementations should honor context cancellation;
calls have a ten-second timeout. Ctrl+C cancels the session context.

## Controls

- Enter sends `WhenBusy: "followup"`; Alt+Enter or `/steer <text>` sends `"steer"`.
- Shift+Enter inserts a newline; Ctrl+J is the fallback for terminals that cannot
  distinguish Shift+Enter. Pasted multiline text is retained.
- Esc requests Stop; Ctrl+C quits (does not implicitly stop the workflow).
- Tab switches transcript/pattern log; PgUp/PgDn scroll the selected panel.
- Ctrl+T collapses/expands the Commands pane.
- `/continue`, `/new [id]`, `/sessions [id]`, `/help` are local commands.
  `/sessions <id>` reconnects to that session. `/new` generates a crypto-random ID.
- `/explore ...` and `/run ...` are submitted unchanged for workflow handling.
- `/demo` sends the guided demo prompt with `Input.Scenario = "demo"`.
  `Options.Demo` submits it once at startup, after starting the event tail.

## Reconnect and rendering

The first `events.Tail` completes before a snapshot query is dispatched. Further
tails run while that query is pending and are buffered with `events.NewReconnect`.
Only events newer than the snapshot's `LastSeq` are replayed. Sequence gaps,
truncation, and buffer exhaustion trigger a fresh snapshot. Epoch and query
identifiers reject obsolete asynchronous results. Offsets are retained for up to
32 recently visited sessions; switching clears the displayed session state.

A 150ms polling tail and independent two-second snapshot refresh ensure the final
answer/status do not depend on a final stream event. A bounded replay cache keeps
newer deltas visible when an in-progress snapshot lags the live stream, without
repeating tool/pattern logs. Snapshot answers are authoritative.

The UI retains 128 entries each for local transcript, pattern log, and command
output (4KiB per entry), 128 assistant segments (64KiB each), and 512 compact
replay events. Pending reconnect buffers use the events package's 4096-event cap.
Display payloads are compacted before buffering, and C0/C1/escape and Unicode
format controls are removed from untrusted display text. Rendering is clipped to
the terminal dimensions; extremely small terminals necessarily hide some panes.

## Intentional seams/limits

- `modal.go` displays snapshot `PendingApproval`; interactive decisions are for
  PR14. No approval or rejection is sent implicitly.
- The Commands pane shows the last two output lines, not a full terminal emulator.
- The transcript combines local messages, the latest bounded snapshot answer,
  and newer streamed content; it does not load the full `TranscriptRef` file.
- The Temporal UI link assumes localhost:8233 and the default namespace because
  the required `Options` contract has no endpoint or namespace fields.
- Shift+Enter/Alt+Enter recognition depends on terminal support; slash steering
  and Ctrl+J provide alternatives.

Tests use fake backends and temporary event stores without a terminal:

```sh
mise exec -- go test ./internal/tui
mise exec -- go test -race ./internal/tui
```
