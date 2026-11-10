# Opt-in live durability tests

These tests run a **real local Temporal CLI server, production worker, workflow,
activities, shared client, CLI, filesystem operations and `go test ./...`
commands**, with the **explicit deterministic mock LLM**. They are not evidence
of Ollama/model quality or cloud behavior. No Temporal test environment or mock
workflow/activity implementation is used.

## Run

From the repository root, with Go and Temporal CLI already installed:

```sh
scripts/test-integration.sh
# Serialized, focused live race check (also instruments worker subprocesses):
scripts/test-integration.sh -race
# Equivalent explicit opt-in:
HARNESS_INTEGRATION=1 HARNESS_MODEL=mock \
  go test -count=1 -p=1 -parallel=1 -timeout=6m -v ./internal/integration
```

The ordinary `go test ./...` path **skips** live tests, does not resolve tools, and
never starts dependencies. An opted-in run fails rather than silently skipping
when the installed CLI cannot be resolved. Resolution uses the production
`config.ResolveTool` (PATH, then `mise which temporal`); it never installs tools.
If mise refuses an untrusted worktree, put the **already installed** CLI directory
on PATH explicitly, or resolve your own mise trust policy outside this test.
Do not run `mise install` just to run the suite.

Only macOS/Linux are supported (the production command implementation uses Unix
process groups). Typical duration is 25–40 seconds after the Go build cache is
warm. Every query/update/startup/child wait is bounded; the outer command has a
six-minute limit. Logs identify the real server address, PID, temporary DB and
fixture PIDs. Server/worker output capture retains bounded 16 KB heads and tails;
all output bytes are inspected for race diagnostics, including markers outside
those retained excerpts or split across writes. On failure the excerpts are
printed before temp files are removed. Keep stdout/stderr externally if evidence
is needed.

## Isolation and ownership

- Each invocation allocates **four distinct ephemeral loopback ports** (gRPC,
  UI, HTTP API, metrics), explicitly excluding 7233 and 8233. It starts only its
  own `temporal server start-dev`, with its own temporary SQLite DB. It does not
  attach to `TEMPORAL_ADDRESS`, default services, Ollama or another task queue.
- Production task queue names are unchanged, but live on the private server.
  Subtests are sequential, with exactly one production worker at a time.
- Server/worker subprocess groups are created and joined by the test. The worker
  reuses the test binary under an explicit env guard and signals readiness only
  after `worker.Start` completes. Fresh clients retry transient startup Query
  failures; an empty/error Query is never treated as settled.
- Every worker shutdown, including replacement and Stop workers, gates the parent:
  normal shutdown requires exit 0; only an explicitly requested, successfully sent
  SIGKILL permits a SIGKILL exit. Other failures/signals and shutdown timeouts fail
  the test. Race diagnostics fail the parent **even for the intentionally killed
  worker**, independently of exit status. Child `GORACE` is deliberately fixed to
  `log_path=stderr exitcode=66 halt_on_error=0`; inherited `log_path`/exit overrides
  cannot silently redirect or hide worker findings.
- The bundled sample is copied to a temp project. Blocked commands run in a
  separate generated Go module. Neither uses the repository as its workdir.
  Every session has its own temp artifact/data directory.
- The fixture executes exactly the supported, approved `go test ./...` command.
  One fast package produces genuine `go test` output; a second records every
  invocation with O_APPEND, creates an O_EXCL positive start marker, publishes
  its process-group/test/descendant PIDs by atomic rename, and blocks. Its child
  is another copy of the generated Go test executable, not a shell command.
  Production command logs/transcripts are never injected or rewritten by tests.
- Cleanup is registered **before approval**, stops the worker before recovering
  final PID ownership, kills only the captured owned command group, joins owned
  server/worker processes, terminates the owned session before another subtest
  can resume it, and removes temp files on normal test failure too. Worker-stop
  errors are reported non-fatally and never skip the separate orphan-group
  cleanup. ESRCH during signaling still joins the waiter (it can race reaping);
  graceful and fallback joins remain bounded to 10 and 5 seconds.
  Like other Go tests, cleanup cannot be guaranteed if the entire test runner or
  machine is forcibly killed; do not externally SIGKILL the runner.
- Ephemeral listener reservations must be closed before the CLI binds (the CLI
  cannot inherit them). Readiness requires both health and a random namespace
  sentinel created only by this server invocation. A port bind collision is a
  test failure, not permission to reuse another service.

## Assertions

1. **Approval durability:** a human write approval stays pending over graceful
   worker shutdown and restart. Original request, action hash, expiry, preview
   and transcript ref remain identical. File bytes remain unchanged until the
   original decision is accepted. Stale IDs, mismatched hashes, and duplicate
   decisions are rejected for the expected reasons. Production after-run asks
   for a distinct test approval and real Go tests verify the copied sample fix.
2. **Worker loss:** observe the command's actual server heartbeat PID and attempt,
   exclusive intent log, positive start marker and live descendant, then SIGKILL
   only the owned worker. Restart it, await the real 10-second heartbeat timeout,
   and require paused/interrupted state, one command schedule/start/attempt,
   MaximumAttempts=1, one execution/intent, and one persisted interrupted tool
   result with the real log tail. **Restart is not process cancellation:** the
   orphan group is explicitly killed by the test, separately from this proof.
3. **Live Stop:** queue a follow-up during the blocked command. Stop must reach
   aborted/paused settlement, have an actual canceled activity in server history,
   and leave command/test/descendant dead **before test cleanup**. The queue stays
   paused through repeated Queries. Explicit Continue resumes retained context
   and the follow-up, with matched transcript tool results and no unsafe rerun.
   Zombie processes count as dead/non-executing; PID absence is not required
   before the OS reaps an orphan.
4. **Reconnect/CLI evidence:** fresh shared-client connections and independent
   production `harness wait` processes recover blocked approvals and final
   answers. The approval test deletes only its own lossy display JSONL while the
   worker is stopped, proving recovery does not require old display events.
   This is not a PTY/interactive Bubble Tea restart test; interactive rendering,
   terminal keystrokes and real-model smoke remain outside this suite.

## Harness regressions (no Temporal required)

```sh
go test -count=1 -p=1 -run 'TestProcess' ./internal/integration
go test -race -count=1 -p=1 -run 'TestProcess' ./internal/integration
```

Owned test-binary subprocesses verify that early/shutdown failures, unexpected
signals, and race-reporting helpers cannot produce a passing parent, while normal
exit and intentional SIGKILL do pass. Synthetic race markers are placed outside
both retained excerpts; race builds also execute an actual data race in a
**grandchild**, including the intentional-kill path. The caller supplies hostile
`GORACE` overrides to prove they cannot hide child reports. Injected signal errors
and the ESRCH/reap window verify waiter joining, failure reporting, and completion
of remaining owned-orphan cleanup. These regressions run in ordinary root tests;
the live suite still requires explicit opt-in and installed Temporal.

Replay-history capture and Continue-As-New coverage are maintained separately;
this suite inspects live history but does not commit replay captures. Root build,
vet, unit and race gates should also be run independently of this opt-in suite.
