# Testing and evidence

[Quickstart](../README.md) · [Operations](operations.md) · [Architecture](architecture.md)

Distinguish unit/test-environment results, live Temporal tests, real provider calls, replay, and hard-worker-loss exercises. Passing one does not establish the others. The original 18-item plan is a design/execution reference; the evidence below states what was actually verified.

## Root gates (no live services required)

```sh
mise trust
mise install
HARNESS_MODEL=mock mise exec -- go build ./...
HARNESS_MODEL=mock mise exec -- go vet ./...
HARNESS_MODEL=mock mise exec -- go test ./...
HARNESS_MODEL=mock mise exec -- go test -race ./...
mise exec -- sh -c 'test -z "$(gofmt -l cmd internal)"'
```

[CI](../.github/workflows/ci.yml) selects `HARNESS_MODEL=mock`, validates tool versions, builds, vets, runs the race suite, and checks formatting on Linux. `mise run test` is root `go test ./...`; `mise run lint` is `go vet ./...`. Tests use Temporal's test environment, HTTP fixtures, temporary files, and owned helper processes rather than requiring Ollama or a live default Temporal server. Leave `HARNESS_TEST_TEMPORAL` unset for these gates: it opts into a real lifecycle test whose managed UI port is still 8233. Also note that `TestReuseAndMock` configures a fake Temporal executable with a default-address fallback; if that helper times out, its failure path can TCP-probe `localhost:7233`. That is not a live workflow test or private-port isolation.

For a constrained or heavily shared machine, serialize package execution without removing race instrumentation:

```sh
HARNESS_MODEL=mock mise exec -- go test -race -p 1 ./...
```

Do not hide an initial failed gate behind a successful retry: retain the failure and state whether the retry was cached, serialized, or narrowed.

### Documentation-pass results on the integrated17 base (`c75db0e`)

- `go build ./...`, `go vet ./...`, `go test ./...`: passed locally on macOS arm64 with the pinned Go toolchain and mock environment.
- Initial `go test -race ./...`: **failed** in `TestResolveTool` (`internal/config`) and `TestReuseAndMock/mock` (`internal/devenv`) at three-second helper timeouts. Other packages passed. These are observed timeout failures, not proof that the default-concurrency gate is universally green.
- Retry `go test -race -p 1 ./...`: passed, with unaffected successful packages reused from Go's test cache and the two failed packages rerun.
- A subsequent uncached default-concurrency `go test -count=1 ./...` hit the same `TestReuseAndMock/mock` helper timeout; its chained race step therefore did not run.
- Final uncached serialized root gates: build, vet, `go test -count=1 -p 1 ./...`, `go test -race -count=1 -p 1 ./...`, and formatting all **passed**. This does not erase the default-concurrency failures above.
- Built CLI help was checked against the documented flag inventory; local documentation links/anchors passed validation. A temporary sample copy failed with `Add(2, 3) = -1; want 5`, as intended. This pass did not run setup, interactive demos, or live server/model/worker-loss tests.

## Intentional failing sample

[Sample project](../examples/sample-project/README.md) is a separate Go module. Root package traversal excludes it. Its `Add` function is intentionally broken; the sample test must fail before a fix:

```sh
sample=$(mktemp -d "${TMPDIR:-/tmp}/harness-sample.XXXXXX")
cp -R examples/sample-project/. "$sample/"
(cd "$sample" && mise exec -- go test ./...)  # expected nonzero before fix
```

Keep this expected failure separate from root gate results. Point `HARNESS_WORKDIR` at the copy before a scripted demo or auto-approved run. Never commit the demo's repaired fixture as a test cleanup; see [safe restoration](operations.md#safe-demo-and-manual-approvals).

## Focused checks and source map

These commands exercise existing packages and need no live services:

```sh
HARNESS_MODEL=mock mise exec -- go test ./internal/workflows -run 'Test(AfterRun|Demo|Inbox)'
HARNESS_MODEL=mock mise exec -- go test -race ./internal/tui ./internal/events
HARNESS_MODEL=mock mise exec -- go test -race ./internal/activities ./internal/tools
HARNESS_MODEL=mock mise exec -- go test ./cmd/harness ./internal/client ./internal/worker
```

Coverage worth reading:

- [Composed demo sessions](../internal/workflows/demo_session_test.go) use actual activities with a mock provider, temporary fixtures, approvals/denials, failing tests, and result summaries. They are not a live real-Ollama end-to-end run.
- [afterRun fixture tests](../internal/workflows/afterrun_fixture_test.go), [recovery/provenance tests](../internal/workflows/afterrun_recovery_test.go), and [execution-error tests](../internal/workflows/afterrun_execution_error_test.go) cover explicit verification, bounded repair, sticky obligations, joined transcript outcomes, and infrastructure errors distinct from positive test exits.
- [Inbox rollover](../internal/workflows/inbox_rollover_test.go) and [provenance](../internal/workflows/inbox_provenance_test.go) test bounded dedup/carry and per-origin repair budgets. Test-environment Continue-As-New checks are not captured-history replay evidence.
- [Command tests](../internal/activities/command_test.go), [streaming tests](../internal/activities/streaming_test.go), and [tool guardrails](../internal/tools/guardrails_test.go) exercise cancellation, heartbeats, logs, and policy. They do not establish sandboxing of arbitrary project code.
- [TUI approval paging](../internal/tui/approval_paging_test.go) tests 80×24 and 40×14 with long content, truncation, reason entry, and scrolling. [Render bounds](../internal/tui/model_test.go) include tiny sizes without claiming usability there. [Reconnect tests](../internal/tui/reconnect_test.go) use fake backends, not terminal automation.
- [CLI regressions](../cmd/harness/cli_regression_test.go), [CLI inbox behavior](../cmd/harness/cli_inbox_test.go), and [session listing](../cmd/harness/session_test.go) test Query-derived settlement, JSONL, cancellation, and unavailable-history handling.

## Live checks and evidence limits

Orchestrator live evidence on integrated17 (`c75db0e`, 2026-11-10), reviewed for this follow-up on integrated18 replay commit `89338c0`:

- Live Temporal/production-worker CLI demos passed with **mock** (7.05s) and local Ollama **`gemma4:e4b`** (56.85s), each on a fresh temporary sample copy with explicit trusted-fixture `--auto-approve`. Both checked all four event patterns, a completed explorer linked to its parent, three approval events, two non-error command completions (scripted test and separate afterRun), corrected source, an authoritative answer with afterRun exit 0/log reference, and an independent `go test ./...` exit 0. The repository sample remains broken. Real provider calls do not establish native model action selection: the parent plan is harness-enforced.
- An **80×24 real PTY, mock-provider** demo passed with three keyboard `a` decisions, verified final state/file, and Ctrl+C exit 0 leaving the external worker alive. This is not a real-model TUI smoke or coverage of every key.
- Self-managed CLI (no `--external-worker`) passed mock startup/cleanup and pure JSONL output; the persisted workflow remained RUNNING after its embedded worker exited. The owned test workflow was then terminated. This is not worker-loss recovery proof.
- Earlier CLI evidence passed JSONL/plain, running-ID reuse/closed-ID rejection, wait, SIGINT/timeout detach, default-off approvals, and JSON listing including unavailable legacy histories. A permanent tool-result artifact-storage fault produced server-confirmed FAILED and authoritative CLI failure.
- These live observations come from integration-local logs, not committed reproducibility fixtures. Repeat using [normal CLI/TUI commands](operations.md#safe-demo-and-manual-approvals), not ignored scratch scripts. No cloud model was used; `gemma4:26b` remains unverified.

### Recorded replay (integrated18)

- [Capture helper and instructions](../scripts/capture-replay/README.md), [replay tests](../internal/workflows/replay_test.go), and [fixture manifest](../internal/workflows/testdata/replay/manifest.json) are committed. Independent review passed a fresh live capture, four positive replays, coverage checks, and a nondeterminism negative control.
- Genuine mock-model Temporal captures contain hello CAN (79 events), demo CAN (450), successor (4), and completed explorer (131). They establish two actual Continue-As-New links, retained duplicate receipts/counters, parent/child linkage with nil child Carry, three exact approval decisions, a real write, and two successful command results with distinct log references. The second test result was cached; this does not prove test bodies executed uncached twice.
- The four-event successor is the full available **open-history prefix**, not a completed session or evidence of future execution. Offline replay registers both workflows with original workflow/run IDs and executes no activities, model calls, commands, or live server requests. The negative control changes a cloned activity name and requires a nondeterminism failure without changing fixtures.
- Only the owned temporary path is sanitized, including decoded JSON payloads. Raw databases/files and referenced transcript/command-log artifacts are not retained; raw checksums are metadata, not independently verifiable raw evidence. The helper iterates history to exhaustion, but these captures do not establish that multiple server pages were needed.

Run the committed offline gates from the repository root (pinned Go and dependencies already installed):

```sh
go test ./internal/workflows -run TestRecordedReplay -count=1
go test ./scripts/capture-replay -count=1
```

For an explicitly opt-in fresh live **mock** capture, choose an output path that does not exist:

```sh
go run ./scripts/capture-replay -capture -out /tmp/new-replay-capture
```

- Capture uses an owned loopback server with non-default gRPC/UI ports, temporary database/project/artifacts, and real activities/commands; it installs no tools. Independent normal and SIGTERM cleanup checks passed. Free-port selection has a bind race, not an atomic listener-ownership guarantee. Do not SIGKILL the helper; crash-orphan cleanup is not established.
- Independent review's initial default-concurrency full suite hit an unchanged resolver-test timeout; serialized full suite and focused race gates passed. This follow-up reran the two offline commands above, not the live capture or the full suite.

### Isolated worker-loss and approval durability

The integrated [live suite](../internal/integration/README.md) uses a real private Temporal server, production workers/activities/shared client/CLI, temporary projects, and an explicitly mock LLM. Both normal and race runs passed on macOS arm64; independent review and re-review approved the functional checks and subprocess/cleanup gate. Ubuntu CI runs both commands below rather than inferring Linux behavior from macOS results:

```sh
mise exec -- scripts/test-integration.sh
mise exec -- scripts/test-integration.sh -race
```

- Four private loopback ports (gRPC/UI/HTTP/metrics), a temporary SQLite DB, and a random namespace ownership sentinel isolate each invocation. Default services on 7233/8233 are not used.
- Approval survives worker replacement with the exact request/hash/expiry/preview and unchanged file. Invalid/stale/duplicate decisions reject; valid approval permits the write and a distinct afterRun test gate.
- After an owned worker is SIGKILLed during an approved command, real heartbeat timeout/recovery yields a paused interrupted result. History, invocation marker and intent log show one unsafe schedule/start/attempt/execution, with a paired persisted tool tail.
- The orphan descendant was observed alive after worker loss and after recovery. The test explicitly kills its captured group: **recovery does not certify exit or kill an orphan**.
- Cooperative Stop cancels and joins the command/test/descendant before cleanup. Retained input stays paused; explicit Continue resumes paired context and queued work without rerunning the unsafe command.
- Fresh clients and built `harness wait` recover authoritative state even after the owned display JSONL is deleted. This is separate from the mock PTY smoke, not an interactive TUI restart test.
- Every worker exit gates the parent. Bounded head/tail output capture scans every byte for race diagnostics, including intentionally killed helpers and markers outside excerpts. Controlled subprocess regressions prove actual race reports and unexpected exits fail the parent; injected ESRCH/EPERM cannot skip orphan cleanup.

Live runs take roughly 25–45 seconds with warm build caches. They require installed Go/Temporal, Bash, `ps`, and a race-capable C toolchain for `-race`; they do not install tools or call Ollama. Ordinary root tests skip the live server suite but run owned test-process regressions. Whole-runner/machine forced death cannot guarantee cleanup. Server restart, power loss, remote filesystem durability and real-model quality are not established by this suite. The [manual graceful restart procedure](operations.md#external-worker-restart-exercise-isolated-server) remains a separate exercise.

Replay fixtures prove current code against those captured histories only. They do not promise workflow-code upgrades across intermediate draft histories. Prefer fresh IDs after code changes; `query/unavailable` for old histories must not be called success or closure.

For live work, isolate your own Temporal server's gRPC/UI ports, database, artifacts, and worker processes. Queues are fixed (`agents`, `llm`, `subagents`), so a different session ID on a shared server is **not** worker isolation. Never restart/kill another experiment's services, and do not attach extra workers to its queues. Keep the final default-port real-model smoke separate from private test servers.
