# Durable paged terminal history

The local TUI uses the persisted transcript for completed conversation, not the
optional display JSONL or a workflow Query answer. Query still owns session
configuration, status, queued input, and approval. No workflow filesystem I/O,
workflow scheduling, artifact format, or dependency version changes are involved.

## Reader contract

```go
activities.ReadTranscriptPage(ctx context.Context, dataDir, workflowID string,
    before, limit int) (activities.TranscriptPage, error)

type TranscriptPage struct {
    Entries []TranscriptRecord // complete records, chronological
    Before  int                // first ordinal in this window
    HasMore bool
    Total   int                // total at the time of this read
}
type TranscriptRecord struct {
    ID      string             // durable transcript identity
    Message llm.Message
}
```

- `before=0` requests the newest page. Otherwise `before` is an exclusive ordinal
  cursor; pass the preceding page's `Before` only when `HasMore` is true. An
  append does not shift older ordinals. Negative/out-of-range cursors and
  nonpositive limits fail; positive limits are clamped to **64**.
- The reader derives the exact SHA-256 artifact name with `TranscriptRef(workflowID)`;
  it accepts neither arbitrary references nor child/log paths from messages.
- The original `State.Session.DataDir` must be absolute. The TUI captures the raw
  value before display sanitization. Missing/relative/legacy metadata is reported
  as unavailable, never replaced with the reconnect client's directory.
- Pinned `os.Root` handles confine directory lookup to DataDir and file lookup to
  its artifacts directory, including symlink replacement races. Escaping
  directory/file symlinks are rejected. Special files are rejected; nonblocking
  open avoids waiting for a FIFO writer. The selected DataDir itself is the trust
  root, not an OS sandbox or remote-filesystem transport.
- Reads retain at most **32 MiB of artifact bytes**, reject more than **65,536
  records**, and validate the entire JSON array, trailing data, UTF-8, roles,
  unique 64-character lowercase hexadecimal message IDs, and at most 32
  unambiguous tool calls per message. Missing files and invalid artifacts are
  errors, not certified empty history. `[]` is an empty history; `null` is not.
- A decoder scans the complete bounded artifact while retaining only the selected
  records and a bounded identity index. This is bounded local paging, **not** an
  indexed constant-time disk reader. The existing atomic-replacement writer
  provides a consistent version per open. Cursors assume that writer's append-only
  semantics; arbitrary external rewrites are not supported.
- Cancellation is checked before opening, around reads, and between decoded
  records. It cannot promise that every filesystem syscall is interruptible.
  Complete selected messages are returned without terminal sanitization or
  pruning. Decoded allocations/temporary render strings add memory beyond the
  raw-byte bound; 32 MiB is not a process RSS limit.

## Controller and reconciliation

`historyState` owns one **64-record contiguous window** (at most the bounded
artifact's content), not an accumulating conversation cache. `/older` replaces
that window; `/latest` returns to the newest page. The header reports its ordinal
range, read-time total, whether older entries exist, and unavailable/cached state.
An older window hides live updates and is not displaced by periodic snapshots.
A page-boundary tool result may explicitly have an unavailable origin.

All reads execute in `tea.Cmd`, with the session context and ten-second timeout.
`historyMsg` carries session epoch, history-request generation, and originating
Query generation. A response must match all three and the pending request.
Session switches, authoritative directory changes, and superseding window reads
invalidate old results. Repeated same-directory status Queries do not invalidate
an in-flight read: otherwise a slow read could starve on every two-second poll.
Periodic refresh waits for the current history read. Explicit navigation can
supersede and cancel that read; its generation invalidates the older reply so End
cannot be swallowed by a pending older-page request. Cancellation is cooperative
around reads, not a guarantee that an already blocked filesystem syscall exits. Errors retain the
last valid window and the independent Query status/approval. `/latest` retries.

Durable records are ordered and identified by message ID, never text or tool-call
ID. Tool results are paired with a call only within the current contiguous window
and the immediately preceding assistant message's scope; user/system/new assistant
records reset that scope. Reused call IDs in later runs remain distinct. Live
assistant `MessageRef`s reconcile with the workflow artifact plus message ID,
using the same hashed opaque reference as the bounded event cache. Identical
content under different IDs remains separate conversation. Matching live entries
are retired when accepting a page, and incoming matching events are suppressed.
A FIFO retains up to **512 completed MessageRef hashes** independently of the
current page and Query snapshot, so evicting a durable row cannot immediately
resurrect its completed stream on replay. This is a bounded identity horizon,
not an all-session historical index: references older than that horizon require
the corresponding durable page for reconciliation. Retirement resets with the
session or original DataDir; directory changes also discard old-root live entries.

Workflow/parent IDs use separate **64-character SHA-256 keys** over their original
bytes (empty stays empty), never sanitized labels for identity comparisons.
The TUI-only event projection retains labels of at most **256 bytes** alongside
those keys. Parent classification compares the session ID in the same hashed
domain. Distinct IDs that render alike remain distinct through retry and
reconnect, including legacy unreferenced segments. The shared event protocol is
unchanged; reconnect validates sequence-only events while the bounded TUI pending
buffer retains the complete display projection.

Live tool/child events are explicitly best-effort and unverified. They never
rewrite or certify a persisted tool result based on a reused call ID. Durable
`delegate` results retain child summaries, child identity and summary references;
this UI does not reconstruct a child's full history from parent events. The live
suffix can be dropped on snapshot reconciliation or event gaps. Historical
messages remain in the artifact and are read again when their page is requested.

Submit receipts do not provide a durable message ID, so there is no unmatchable
optimistic user row. Before real history is available, a Query answer is labeled
as a **fallback, not a journal record**. Once a nonempty durable window is loaded,
that fallback is suppressed rather than matched by message content.

## History controls

- `/older`: replace the current page with the preceding one, anchored at its top.
- `/latest`: leave record details, read the newest window, and jump to its bottom.
- `/detail <record number>`: select a numbered record from the loaded window and
  show its captured content/arguments/result metadata in the scrollable viewport.
- `/journal`: return from selected detail to the loaded journal.
- PgUp/PgDn or mouse wheel: browse the journal or selected detail; End follows
  latest and clears unseen updates. Ctrl+Up/Down selects, Ctrl+O expands/collapses.
  Ctrl+T collapses tool bodies in the overview without hiding outcome labels.
- Ctrl+L or `/log`: open a validated command log. Ctrl+Left/Right or
  `/logprev` / `/lognext` page captured bytes. See [log bounds](terminal-journal.md#safe-full-log-browsing).
- Tab retains the optional pattern log. `/help` lists the controls. Existing
  Enter, Alt+Enter, Esc, Ctrl+C and exact approval bindings are unchanged.

The overview displays at most **4 KiB per durable record**, with an explicit
truncation marker. All 64 record headings remain accessible; use `/detail` for a
long record. Selected detail permits **504 KiB** of sanitized text, including
captured multiline output and log references. The total journal viewport text is
bounded to **512 KiB**. The full bounded record remains in the page cache even if
its display is truncated. Existing live caches remain bounded to 128 segments
(64 KiB assistant text / 4 KiB tool event), 512 compact replay events, and the
existing reconnect buffer cap. There is no infinite scrollback-memory promise.

No log reference is automatically opened, no command is executed by inspecting a
record, and no historical write diff is reconstructed from today's filesystem.
Requested write content is labeled as requested arguments, not an original diff.
Persisted structured command results require a captured exit code before showing
success; prose and live ToolEnd events do not verify commands. Legacy/unstructured
results have unknown outcomes. Successful `read_file` content historically has no
separate envelope, so even JSON-shaped file data is treated as captured content,
not trusted outcome metadata.

Outcome decoding selects two separate writer schemas: an ordinary `tools.Result`
requires a nonempty matching `toolCallId`, string `output`, boolean `isError`,
well-typed optional `interrupted` and a completely valid string-valued `details`
map. Command exit evidence comes only from `details.exitCode`; optional
`details.executionError` must be `"true"` or `"false"`. Verification uses the
identity-less `workflows.verificationResult` shape: `exitCode` (integer or null),
`isError`, `interrupted`, `executionError`, `logRef`, `tail`, and `truncated`.
All seven fields must be present and correctly typed, with a nonblank log ref.
A present identity, even null/malformed/mismatched, cannot fall back to verification.
Both schemas use exact canonical keys, not case-insensitive struct decoding.
Duplicate member names (including JSON-escaped equivalents) in the envelope or
ordinary details, and case-fold aliases of recognized envelope/detail fields, are
refused. Identity is checked after canonical decoding; exit/failure interpretation
uses the same completely validated details map. Unrelated extension values are
opaque and never supply execution evidence.

Mixed ordinary/top-level verification evidence, malformed/null discriminators
(including whitespace-wrapped null), partial maps, and malformed explicit exit
codes cannot certify success. Failure and interruption override zero exit;
missing/null exit never means zero. When certification is unavailable, the original
content is retained rather than reserialized, preserving contradictory duplicate
values, malformed/null output, details and log references (subject to the normal
terminal sanitization/display bounds). No workflow writer or artifact schema is
changed.

The [native terminal journal](terminal-journal.md) adds measured pinned-composer
layout, phase/mode/timing, explicit follow/unseen updates, frozen reading windows,
record-level resize anchors, mouse scrolling and bounded command-log paging.
Private real PTY/live-service qualification and independent final review remain
controller-owned; deterministic rendering tests do not establish that evidence.
