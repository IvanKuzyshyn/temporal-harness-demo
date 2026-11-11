# Operations

[Quickstart](../README.md) · [Architecture](architecture.md) · [Testing](testing.md)

Run examples from the repository root. Services and projects must be trusted. Use a temporary sample copy for every scripted or auto-approved exercise; do not let examples silently fix the committed broken baseline.

## Configuration and precedence

[Config source](../internal/config/config.go) and [mise defaults/tasks](../mise.toml):

- `HARNESS_MODEL`: `gemma4:e4b`. Literal `mock` selects the deterministic mock provider and skips Ollama setup. Optional `gemma4:26b` is not hardware-verified here.
- `TEMPORAL_ADDRESS`: `localhost:7233`; default namespace. No CLI namespace or task-queue override is exposed.
- `OLLAMA_HOST`: `127.0.0.1:11434`; the worker accepts a host:port or HTTP(S) URL. Use local endpoints; there is no cloud-provider selection.
- `HARNESS_WORKDIR`: project root; unset/empty defaults to the invocation's current directory, including through `mise exec --`. `run`, `tui`, and `demo` accept `--workdir`: nonempty flag > nonempty environment > current directory. Relative selections resolve against that current directory; `Config.Workdir` is absolute. Mise tasks may change directory to their task root: prefer `mise exec -- /absolute/path/to/bin/harness tui` from your project, or an explicit `--workdir`. No automatic fixture copy or project write occurs on startup.
- Session configuration is fixed by the original workflow input. Queries expose optional `session: {model, workdir, dataDir, scenario}`; restoration recomputes it from that input, not Carry or reconnecting client flags, without activities. The TUI retains this metadata through snapshots, shows original model/root and warns on selection mismatch; legacy missing metadata is explicitly unknown. `run` prints sanitized original configuration and mismatch diagnostics to stderr before submitting to an existing session, or after its first successful query for a new one. Missing legacy metadata is explicitly unknown. JSONL stdout remains pure, and the final `run --json` summary also preserves `session`. Start a new session to change roots/models; reconnecting is not reconfiguration.
- `HARNESS_DATA_DIR`: `.data` relative to the invoking directory. Artifacts, event streams, logs, and managed Temporal database live here.
- `HARNESS_APPROVAL_TIMEOUT`: `5m`; `HARNESS_HANG_TIMEOUT`: `120s`; both must be positive Go durations.
- `HARNESS_MAX_TURNS`: `12`, a positive integer. The CLI supplies a Continue-As-New threshold of 100 turns; there is no flag for it.

Supported `--model` flags override the environment; nonempty environment values override code defaults. Mise's environment templates preserve caller overrides. Workdir/data paths become absolute on load. Tool lookup prefers the caller's `PATH`, then bounded `mise which`; it does not silently install tools. Use `mise exec -- ...` for pinned tool selection.

The initial workflow input fixes model, workdir, data directory, budgets, and timeouts for that session. Reattaching/submitting later with different client flags does **not** change them; the TUI labels the original queried model/root. Each worker dispatches by the activity request's original model, not the launching client's selection: exactly `mock` uses mock; all other model strings go unchanged to the explicitly configured local Ollama endpoint. Provider/endpoint errors are reported, never replaced with another provider. Ollama construction is lazy, so mock-only processing requires no valid/running Ollama setup. Reconnecting with `--model mock` does not provision Ollama for an original real-model session: its endpoint/model must still be available. Workers sharing queues must have compatible local endpoint and filesystem access; this does not provide remote workspace transport.

## Services and ownership

```sh
mise trust
mise install
mise run setup                  # build + doctor --fix
./bin/harness doctor            # health/model checks only
./bin/harness doctor --fix      # start missing local services / pull selected model
mise run demo -- --workdir "$sample" # explicitly select a disposable fixture copy
```

- Setup's successful standalone `doctor --fix` intentionally leaves newly started services alive. It reuses healthy existing services without taking ownership; it refuses to replace a listening unhealthy Temporal endpoint or auto-start against a non-loopback address.
- `demo`, `tui`, and non-external `run` start dependencies as needed and an embedded worker. On exit, they stop only service process groups they started and their own worker; they do not kill reused services.
- `tui/demo --external-worker` skips the embedded worker **but still performs dependency setup**. `run --external-worker` skips both dependency setup and embedded-worker startup. `worker` connects to an existing server; it does not start dependencies.
- Ctrl+C in a TUI quits/detaches, not a Stop Update. If that invocation owned the only worker or server, progress pauses until those resources are restored. For a meaningful worker-restart exercise, start server and worker separately.
- Managed Temporal startup uses `<dataDir>/temporal.db` and a fixed UI port **8233**, even when `TEMPORAL_ADDRESS` changes. For parallel experiments, start a separate server manually with **both** private gRPC and UI ports; do not use automatic startup as a port-isolation mechanism.
- Open the Temporal UI at your server's actual UI port/namespace. The terminal journal no longer advertises a hard-coded localhost link that could point at a different server.

If Ollama installation fails, install with `brew install ollama` on macOS or follow [Ollama's Linux instructions](https://ollama.com/download/linux), then rerun doctor. The pinned Temporal CLI must be installed even when reusing a server: doctor uses its gRPC health command, not just a TCP connection check. `HarnessStatus` search-attribute registration is best effort; listing does not depend on it.

## CLI contracts

[Dispatcher](../cmd/harness/main.go) and per-command source are the reference. `--help`/`-h` is accepted on each command below (`session list --help` for listing). Unknown commands, flags, extra positional arguments, invalid input, and runtime failures exit **1**; successful commands/help exit **0**. No distinct timeout/failure exit-code taxonomy is implemented.

- `harness demo [--session ID] [--model MODEL] [--autoplay] [--external-worker]`
- `harness tui [--session ID] [--model MODEL] [--autoplay] [--external-worker]`
  - Demo shows a guided banner; neither command submits on startup unless `--autoplay` is set. Autoplay starts the guided scenario and does not approve it. Empty session ID generates a new ID.
- `harness worker [--model MODEL]`
  - Polls `agents`, `llm`, `subagents`; Ctrl+C stops this worker, not persisted workflows.
- `harness doctor [--fix]`
  - No model flag: use `HARNESS_MODEL`.
- `harness run -p TEXT [--session ID] [--model MODEL] [--json] [--auto-approve] [--external-worker] [--timeout DURATION]`
  - Prompt must be nonempty; no positional arguments. Default timeout is `5m`, positive only. Prompt is not shell-evaluated by the harness.
  - Submits a follow-up, then polls authoritative state until the submitted work has settled (including queued automatic work), not merely until an event says `agent_end`.
  - **`--auto-approve` is explicitly risky**, default off. It warns on stderr and approves current Query-bound requests with the OS username and reason `explicit CLI --auto-approve`. It can approve other pending work in an existing session while waiting; use a fresh session and trusted temporary fixture. It is not a durable blanket approval policy after the CLI exits.
- `harness session list [--json]`
  - Reads paginated Temporal visibility and queries each session; no server startup. Rows report agent last-state, **not Temporal execution status**. An `idle` row can still have `failure`.
  - Typed Query failures (including replay failures of old drafts) produce `query/unavailable` and do not suppress healthy rows. Missing/closed-unqueryable executions produce `closed/unavailable`. Neither label claims successful completion; other infrastructure errors still fail the command.
- `harness wait SESSION_ID [--until settled|blocked] [--timeout DURATION]`
  - Defaults: `settled`, `5m`. Also accepts flags before the ID. `blocked` means a current undecided approval, not any possible stall. It prints the request ID when reached; it does not decide it. `settled` uses authoritative quiescence and failure metadata.
- `harness approve SESSION_ID REQUEST_ID [--user USER] [--reason REASON]`
- `harness deny SESSION_ID REQUEST_ID [--user USER] --reason REASON`
  - User defaults to `$USER` and must be nonempty; denial requires a nonblank reason. Both read the current action hash via Query and print `recorded` only after the Update completes. Local username is not authentication.

`run`/`wait` timeout, SIGINT, or SIGTERM cancels the **client wait**, never sends Stop. They return an error/exit 1 on cancellation. Stop is a recorded workflow Update (TUI Esc); there is no standalone `harness stop` command. Use a fresh ID after an execution closes: Update-with-Start rejects reusing a closed ID, even though open settled sessions accept further messages.

### JSONL and completion

`run --json` emits one JSON object per stdout line: best-effort [event envelopes](events.md), then a bounded authoritative summary on observed settlement:

```json
{"type":"session_state","id":"session-example","status":"idle","turn":1,"answer":"Example final answer"}
```

Optional `failure` contains `reason` and optional `detail`; `answer` is omitted when empty. This summary is **not** an event envelope: it has no stream `seq` or `runId`. `session list --json` emits the same summary shape per session but omits answers. Diagnostic text, SDK logs, setup progress, and the auto-approval warning stay on stderr for `run --json`.

- Consumers must trust the final Query-derived `session_state`, not child completion, streamed prose, or a parent terminal event alone.
- Failed settlement still emits its summary, then exits 1 with the error on stderr. Timeout/query/setup errors can exit without a final summary; do not treat EOF as success.
- Plain `run` streams parent message deltas and then prints the authoritative answer, so some prose can appear twice. `wait` is plain text only; it has no `--json` flag.

## TUI controls

Bindings: [model](../internal/tui/model.go), [commands](../internal/tui/commands.go), [approval modal](../internal/tui/modal.go).

- **Enter**: send a follow-up; **Alt+Enter** or `/steer TEXT`: steer at the next safe boundary.
- **Shift+Enter** or **Ctrl+J**: newline. **Ctrl+V**: read the system clipboard; bracketed pasted multiline text is also supported. Terminal support for modified Enter varies.
- **Esc**: Stop current run (also while blocked on approval). **Ctrl+C**: quit/detach without Stop.
- **Tab**: journal/pattern log; **PgUp/PgDn** or **mouse wheel**: scroll without following updates; **End**: follow latest and clear unseen-update count. **Ctrl+Up/Down** selects a record; **Ctrl+O** toggles detail. Normal typing/Up/Down still edit the draft. **Ctrl+T** collapses tool bodies without hiding outcome meaning.
- **Ctrl+L** or `/log`: selected command's captured log; **Ctrl+Left/Right** or `/logprev` / `/lognext`: previous/next 16 KiB byte page. These are read-only controls, not shell execution. `/metadata` exposes sanitized original identities in scrollable rows.
- `/older`: preceding durable page; `/latest`: retry history / return to newest page and bottom. `/detail NUMBER`: inspect a numbered record from the loaded window; `/journal`: return from detail.
- `/help`: show controls; `/continue`: resume retained work/unanswered context; `/new`: generate a new session ID.
- `/new ID` or `/sessions ID`: select/reconnect to that ID; `/sessions`: list known session IDs. Selecting a missing ID does not create the workflow until a message is submitted.
- `/demo`: explicit guided-scenario token, also submitted by autoplay (including on existing sessions). New workflow runs do not infer this mode from natural-language fixture prompts; explicit initial `Scenario=demo` applies to the first logical run only. The Temporal `explicit-demo-routing` gate preserves the old aliases only on its `DefaultVersion` branch for existing recorded histories. An unspecified client scenario is not a conflicting configuration selection. `/explore TASK` and `/run COMMAND` are forwarded unchanged to workflow/model handling; they are not direct shell execution shortcuts.
- Approval screen: **a** approve; **d** enter denial mode; type a nonblank reason then **Enter** to deny. **Backspace** edits the reason; bracketed paste is supported. **PgUp/PgDn**, **Up/Down**, **Home/End** scroll action/preview/arguments even during reason entry or an in-flight decision. Esc still means Stop, not “dismiss this approval.”

Use **80×24** or larger for normal viewing; the composer stays in reserved bottom rows. Approval paging is tested at **80×24 and 40×14**, including truncation markers and denial entry. Below **40×12**, a resize warning replaces approval and neither approve nor deny can dispatch (Esc/Ctrl+C remain available). General rendering is bounds-tested down to 0×0/1×1; that is clipping evidence, not a claim those sizes are usable. Preview/arguments are bounded and can be truncated. The journal loads one contiguous window of up to 64 complete persisted records; older pages replace it rather than accumulating unbounded memory. Missing optional display JSONL does not erase history. Query owns status/approval, while its answer is only a labeled fallback until history loads. Tool output is scrollable in details; validated command logs can be paged beyond the model tail, with explicit byte/display limits and unavailable indicators. Frozen browsing retains the current window while polling tracks new updates; End refreshes it. History requires the original absolute session DataDir; unavailable metadata never silently falls back to the reconnect client's directory. See [durable history](durable-history.md) and [native terminal journal](terminal-journal.md) for reader bounds, reconciliation, truthful progress, and final private PTY qualification steps.

## Safe demo and manual approvals

```sh
sample=$(mktemp -d "${TMPDIR:-/tmp}/harness-sample.XXXXXX")
cp -R examples/sample-project/. "$sample/"
export HARNESS_WORKDIR="$sample"
# Intentionally fails before the demo; root harness tests do not include this module.
(cd "$sample" && mise exec -- go test ./...)
./bin/harness demo --autoplay
```

Review and decide the write, scripted test, and separate afterRun test requests. Provider streaming is labeled draft commentary in the forced demo; only recorded tool outcomes support a success claim. Denials/nonzero exits must remain visible, and unrelated fixture content is not overwritten by the plan.

For a noninteractive run, choose a fresh ID and keep approvals manual by default:

```sh
id="session-manual-$(date +%s)-$$"
./bin/harness run --session "$id" -p /demo --json --timeout 10m
# In another terminal, using the same Temporal address:
./bin/harness wait SESSION_ID --until blocked --timeout 2m
./bin/harness approve SESSION_ID REQUEST_ID --user "$USER"
# Or:
./bin/harness deny SESSION_ID REQUEST_ID --reason "Do not execute this action"
```

To repeat the trusted-fixture auto-approved CLI path, use a fresh temporary sample copy as above for each model run:

```sh
./bin/harness run --model gemma4:e4b -p /demo --json --auto-approve --timeout 10m
# Repeat with another fresh copy for mock:
./bin/harness run --model mock -p /demo --json --auto-approve --timeout 10m
```

- Both model paths passed the live four-pattern CLI checks with three approvals and separate scripted/afterRun successes; the parent plan was harness-enforced. The mock 80×24 TUI smoke passed three keyboard `a` decisions and Ctrl+C detach with an external worker surviving. See [exact evidence boundaries](testing.md#live-checks-and-evidence-limits).
- For isolated genuine history capture and offline replay, use the [committed replay helper](../scripts/capture-replay/README.md) and [replay commands](testing.md#recorded-replay-integrated18). Its successor fixture is an open prefix, not a completed execution.

Use the printed/queried request ID for each distinct approval, not a copied old ID. After all sessions using the temporary project are stopped, discard that copy. If the repository fixture was changed accidentally, inspect `git diff -- examples/sample-project/calc/calc.go`, save any wanted edits, then use `git restore --source=HEAD -- examples/sample-project/calc/calc.go` **only if discarding that file's changes is intended**. Do not use `git reset --hard` or reset unrelated work.

## External-worker restart exercise (isolated server)

This is a reproducible **manual procedure, not a claimed W6 worker-loss test result**. Choose unused private ports; never borrow another experiment's server/queues. The example uses 17233/18233, not the default 7233/8233. Start from a built binary:

```sh
lab=$(mktemp -d "${TMPDIR:-/tmp}/harness-lab.XXXXXX")
cp -R examples/sample-project "$lab/project"
export HARNESS_DATA_DIR="$lab/data" HARNESS_WORKDIR="$lab/project"
export HARNESS_MODEL=mock TEMPORAL_ADDRESS=127.0.0.1:17233
mkdir -p "$HARNESS_DATA_DIR"
mise exec -- temporal server start-dev --ip 127.0.0.1 --port 17233 \
  --ui-port 18233 --db-filename "$HARNESS_DATA_DIR/temporal.db"
```

Leave that server in its own terminal. In additional terminals, export the **same absolute** data/workdir values and Temporal/model settings, then run:

```sh
# Worker terminal:
./bin/harness worker --model mock
# Client terminal (fresh ID; retain it for reconnect):
./bin/harness tui --external-worker --model mock --session session-restart-UNIQUE
```

Submit a message or `/demo`, quit the TUI, stop only your worker with Ctrl+C, restart it with the same command, and reconnect with the same session ID. The separate server/database and artifacts survive client/worker exit. Use the manually chosen UI at `http://127.0.0.1:18233`. Shut down only your owned terminals/processes when finished.

Hard SIGKILL is a different failure mode: an unsafe command is never automatically retried, and recovery can report an interrupted result with retained log tail. It **cannot certify exit or kill a preexisting orphan group**. Do not SIGKILL during arbitrary project commands as a casual cleanup technique. The reviewed [opt-in live suite](../internal/integration/README.md) exercises hard loss on its own private server and generated fixture, with explicit orphan cleanup. Its normal and race evidence is recorded in [testing](testing.md). Captured replay is a different proof and does not establish orphan termination.

## Security and durability limits

- [Path/argv guards](../internal/tools/security.go) reject traversal/private paths, resolve symlinks, and use rooted filesystem operations for local tools. They do not promise immunity to every concurrent hostile filesystem swap. Workdirs, artifacts, and services must be trusted.
- The command allowlist is deliberately narrow: `go test ./...`, `go build ./...`, `go vet ./...`, path-constrained `ls`/`cat`, `git status --short`, and `git diff --no-ext-diff --no-textconv [-- PATH ...]`. No shell, arbitrary flags, pipelines, or shell expansion. Execution repeats checks and constrains Git configuration, but approved Go/project code can still execute arbitrary programs and access the worker's environment/credentials. **This is not a sandbox.**
- Approvals bind canonical action arguments, not an immutable snapshot of the filesystem. Previews are bounded observations and may be stale. `$USER`/`--user` is an audit label, not verified identity or authorization.
- Unsafe activities use one attempt. Commands record intent before starting; recovery never re-executes an uncertain intent. Graceful cancellation attempts process-group teardown; hard worker death can leave orphan groups. Inspect only your owned processes before manual cleanup; never use broad `pkill` against shared services.
- Preserve both Temporal DB and artifact directory. Deleting/moving either can break reconstruction or transcript access. Bounded transcript/result persistence exhaustion fails closed, not as a successful settlement. JSONL display has no fsync durability guarantee.
- Local advisory-lock/filesystem behavior is assumed. There is no multi-host shared-storage guarantee, indefinite idempotency window, or workflow-code versioning promise across intermediate drafts. Prefer fresh IDs after code changes. Replay fixtures establish compatibility only with their captured histories.
