# Testing and evidence

[Quickstart](../README.md) · [Operations](operations.md) · [Architecture](architecture.md)

Distinguish unit/test-environment results, live Temporal tests, real provider calls, replay, and hard-worker-loss exercises. Passing one does not establish the others. The original plan's 18-item ledger is not an all-complete claim.

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

## Live checks and pending final evidence

Earlier integration evidence supplied to this documentation pass establishes:

- CLI JSONL/plain output, running-ID reuse, closed-ID rejection, manual approval blocking, SIGINT/timeout detach, auto-approval, and listing healthy plus unavailable legacy sessions.
- A live **mock** four-pattern approval session at an earlier integration point. Its approval count is not a contract for the composed afterRun version, which can ask for a separate verification approval.
- Real local default-model (`gemma4:e4b`) calls, including a read-only explorer child. This does not establish a final all-pattern real demo or `gemma4:26b` hardware support.

Those earlier observations were reviewed from integration scratch logs; they are not checked-in reproducibility fixtures. Use the commands in [operations](operations.md), not ignored scratch scripts, to repeat the manual exercises.

**Pending W6/orchestrator evidence — do not treat as passed:**

- Final integrated all-pattern real-Ollama demo, including truthful scripted/afterRun test outcomes and distinct approvals.
- Captured Temporal history fixtures and their replay test paths/results. No fixture or replay command path is asserted at this base; the replay owner must supply the actual committed deliverable.
- Live durability/hard-worker-loss automation, actual script/test paths, isolated server settings, and interruption/recovery results. The manual external-worker procedure is a reproduction guide, not the missing result.

Replay fixtures, once supplied, prove current code against those captured histories only. They do not promise workflow-code upgrades across intermediate draft histories. Prefer fresh IDs after code changes; `query/unavailable` for old histories must not be called success or closure.

For live work, isolate your own Temporal server's gRPC/UI ports, database, artifacts, and worker processes. Queues are fixed (`agents`, `llm`, `subagents`), so a different session ID on a shared server is **not** worker isolation. Never restart/kill another experiment's services, and do not attach extra workers to its queues. Keep the final default-port real-model smoke separate from private test servers.
