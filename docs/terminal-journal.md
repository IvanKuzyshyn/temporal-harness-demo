# Native terminal journal

This is the existing Go/Bubble Tea application, not the browser design reference.
The pinned Charm dependencies, Backend operations, and exact approval binding are
unchanged. No renderer performs filesystem or network I/O.

## Layout and controls

At **80×24 and 120×40**, compact original session/model/project metadata and real
activity sit above one chronological journal. The composer occupies reserved
bottom rows. Width-limited chrome is explicitly elided rather than wrapping over
the prompt. `/metadata` adds sanitized original metadata as scrollable journal
rows (4 KiB per field); original unsanitized metadata remains in the State Query.
Client selections are labeled unknown/requested until authoritative metadata is
available. Mismatch comparison uses raw values, not their sanitized rendering.

- **PgUp/PgDn** or **mouse wheel** scroll without editing the draft. Browsing is
  explicit; reaching the bottom by scrolling does not silently resume following.
- **End** or `/latest` exits detail/log browsing, reloads the newest 64-record
  window, follows live updates, and clears the unseen-update indicator.
- **Ctrl+Up/Down** selects a durable record; **Ctrl+O** expands/collapses captured
  details. `/detail NUMBER` and `/journal` are terminal-compatible fallbacks.
  Selection is separate from normal Up/Down and ordinary text, which still edit
  the composer. Detail content uses the same scroll keys/wheel.
- **Ctrl+T** collapses tool bodies without hiding captured outcome labels.
  **Tab** visits the secondary pattern log and returns to the same journal,
  detail, or captured-log view. It does not enable follow-latest or clear unseen
  updates. Pattern visits start at the top; scrolling patterns does not change
  the underlying journal's follow policy. `/older` replaces the window with the
  preceding page; it is not a claim that truncated output can be recovered.
- **Ctrl+L** or `/log` opens a selected command's validated captured log.
  **Ctrl+Left/Right** or `/logprev` / `/lognext` page backward/forward through bytes;
  PgUp/PgDn/wheel scroll inside that page. Ctrl+O returns to the journal.
- **Enter** sends, **Alt+Enter** or `/steer TEXT` steers, **Shift+Enter/Ctrl+J**
  inserts a newline, **Ctrl+V** pastes. **Esc** sends Stop; **Ctrl+C** detaches.
  `/help` lists controls. Navigation never submits an approval or executes a tool.

Browsing freezes the bounded display window and its durable page, preserving
stream drafts even when snapshots retire/rebuild live entries. A durable record ID
plus logical line and display-cell position preserve the actual reading content
across resize. Reflow uses the pinned viewport's cell-sliced wrapping, including
wide and combining characters, and bounds restoration inside the anchored record.
The original cell is retained across repeated width changes rather than rounded
down anew at each width. Legacy/live-only text uses a position in the same bounded
frozen display window, without inventing a durable identity.

There is one underlying journal/detail/log window, not a cache of panel visits.
While patterns are visible it remains retained, including a frozen journal's old
page or a log's byte page and read position. Patterns take precedence in both
chrome and body. Returning restores the underlying anchor at the current width;
a deliberately requested log/history page that finishes while hidden is ready on
return, without taking over the pattern view. Session/directory identity changes
and explicit latest navigation discard the prior secondary-view state.
Polling still observes the latest total without evicting the reading page. The
indicator counts **updates**, not unique messages: new stream events plus newly
observed durable records can both contribute. It saturates at 999999; repeated
identical history reads do not increment it. There is no unbounded scrollback
cache or exact unseen-message claim. The window can be stale until End is used.
Existing explicit submission and information-command behavior is unchanged:
sending/steering or displaying `/help`, `/metadata`, or `/sessions` resumes follow
policy. Rendering/repositioning alone and Tab do not; End or `/latest` additionally
exit secondary views and explicitly request the newest durable page.

## Progress and evidence

Optional `State.Phase`, `Mode`, `PhaseStartedAt`, `RunStartedAt`, and
`RunFinishedAt` are observation-only fields. Workflow boundaries use
`workflow.Now`, without adding activities, timers, or ordering changes. Phases
include context, generating, tools, awaiting approval, verification, settled and
failed. Actual per-run routing labels normal versus **demo (harness-enforced)**;
the session's original scenario does not permanently relabel subsequent runs.
Missing legacy fields stay unknown. Active work animates even with no Query in
flight; polling alone does not animate agent activity. Finished run elapsed time
stops advancing. Tools/children come from authoritative Query state, not counts
inferred from prose. Last observed parent heartbeat age uses the event timestamp,
not successful Query time; no observed heartbeat means **unknown**. A stale
heartbeat is not proof of a dead workflow or a successful command.

Succeeded/failed/denied/interrupted/unavailable labels retain meaning without
color; restrained green/red/amber is only an accent on validated outcome metadata.
Read content is **evidence, not execution verification**. Settlement or an
assistant's claim is never converted into a successful test outcome. Canonical
parser/identity/mixed-schema protections described in [durable history](durable-history.md)
also apply when extracting a command log reference.

## Safe full-log browsing

`activities.ReadCommandLogPage(ctx, dataDir, workflowID, ref, offset)` accepts only
`artifacts/<sha256(workflowID)>/cmd-<64 lowercase hex>.log`. It uses the original
absolute session DataDir. Pinned root/directory handles, non-symlink checks and
before/after file identities reject cross-session aliases, escaping references,
replacement races to different identities, and special files. The selected DataDir
is still a trusted local root, not a sandbox against arbitrary code or disk edits.

One **16 KiB byte page** is retained. Reads use seek, do not load the whole log,
and expose at most the first **64 MiB**; larger artifacts have an explicit browsing
limit indicator. More available pages, source-tail truncation, display truncation,
and unavailable artifacts are separate states. UTF-8 split at page boundaries is
replaced with `?`; byte offsets are reported, not invented line numbers. Pages can
show middle output beyond the model's 50-line/8 KiB tail. This is captured plain
text, not a terminal emulator or a shell/path opener. No historical diff is rebuilt
from current files. Missing logs remain unavailable; no fabricated content.

Reads execute only in cancellable ten-second `tea.Cmd`s. Epoch, monotonically
increasing request generation, raw reference, record ID and pending state reject
stale responses, including reopening the same record. Cancellation is checked
around reads, not a promise of interruptible filesystem syscalls. Artifact edits
between separate page requests are not a historical snapshot guarantee.

## Approval and qualification boundary

Approval preempts the composer so typing is never presented as normal drafting
while letters can decide. At **40×12 or larger**, fixed controls surround a paged
captured preview. Below that size the screen says **RESIZE** and refuses both
approve and denial dispatch; Esc/Stop and Ctrl+C/detach remain available. Denial
reason survives resize. Existing re-query, expiry, request/hash and user checks
remain required after the size interlock.

Tiny normal layouts prioritize the prompt and omit chrome; dimensions too small
to show meaningful text are only bounds-tested, not advertised as usable.

Deterministic tests cover geometry/drafts, frozen anchors, unseen counts, input
separation, mouse events, log paging/scope, stale results, truthful clocks/modes,
parser safeguards and tiny approval refusal. They are **not real PTY evidence**.
The controller owns final private-server normal/race durability, ordinary real-model
repository probes, disposable mock guided demo, actual PTY capture and independent
reviews. Existing older live evidence must not be relabeled as final-tree testing.

For final manual/PTy qualification on an owned private server/worker:

1. Use temporary projects and a fresh ID; connect at 80×24, then 120×40. Keep a
   multiline draft while a run streams; confirm prompt, identity and progress.
2. Page up and scroll with the wheel; allow new output/snapshots; confirm stable
   reading, BROWSING/unseen updates, then End/LIVE and unchanged draft. Browse deep
   inside a long wrapped record with following records; resize 80→120→80 and
   confirm the actual visible content, not only a stored selection. Visit patterns
   from `/older` while new history arrives, and return to the same old page and
   unseen count. Repeat from scrolled detail and captured-log byte pages; verify
   truthful chrome/body, preserved offset, and End or `/latest` as the reset.
3. Select/expand a tool result. For a known long captured command log, page to a
   middle sentinel beyond its tail. Reconnect and repeat using persisted records.
4. On a disposable guided fixture, review actual approvals. Resize below 40×12,
   press `a` and denial Enter, and verify no decision. Restore size, approve or
   deny with a reason, and verify the exact current request was decided.
5. Ctrl+C detaches; reconnect through the independently owned worker. Never stop
   default services, silently approve arbitrary repository code, or infer actual
   keyboard behavior from a model-only render test.
