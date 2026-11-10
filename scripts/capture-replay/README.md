# Opt-in genuine Temporal replay capture

From the repository root, with the pinned tools already installed:

```sh
go run ./scripts/capture-replay -capture -out /tmp/new-replay-capture
```

The output directory must **not** exist. Without `-capture` the command refuses
to run. `config.ResolveTool` uses PATH then bounded `mise which`; it installs
nothing. Trust this worktree's `mise.toml` yourself if mise requires it.

This macOS/Linux helper creates its own loopback Temporal dev server, ephemeral
non-default gRPC and UI ports, process group, temporary SQLite database, project
copy and artifact directory. It excludes 7233/8233 and has no fallback that
intentionally reuses an existing server or stops another worker. The hard-coded workflow queue names exist only inside
this separate server. Ctrl-C/SIGTERM and a four-minute deadline cancel the
capture; cleanup joins workers and terminates only the owned server process
group (TERM, then KILL after five seconds). Do not use SIGKILL on the helper.
There is a free-port selection/bind race: ports are not atomically reserved and
listener ownership is not authenticated against an adversarial takeover. A bind
failure is not a reason to reuse another server. The model is always mock; activities, filesystem
writes and both approved `go test ./...` commands are real. No Ollama or cloud
model is used.

The capture submits hello using the shared Update-with-Start client, observes
an actual new run ID, resubmits the same RequestID/text, and checks its receipt,
unchanged logical-run/turn counts and empty inbox. Receipt retention is checked
in the actual successor input: public Query intentionally omits that ledger.
It then submits `/demo` to that existing session and approves each exact current
request ID/hash, including the separate after-run test gate. It observes the
verified final state and next Continue-As-New. Full paginated histories include
the first CAN run, demo CAN run, idle successor (an open history prefix), and
actual completed explorer child; the child's input is checked for nil Carry.

`manifest.json` records versions, source HEAD, command, observed query states,
approvals, workflow/run IDs, event counts and SHA-256 hashes. Raw hashes are of
pre-redaction protojson, not a Temporal wire encoding; raw files and the owned
database are not retained. Only the temporary root is replaced with
`/replay-capture`, including **decoded json/plain payload bytes** (base64 in the
history JSON). Worker identity is fixed at source. IDs, timestamps, results,
action hashes and event structure are unchanged. Artifact references are
recorded evidence; the referenced transcript/log files are not bundled.

The helper immediately replays using SDK `worker.NewWorkflowReplayer`, both
workflow registrations and `OriginalExecution` IDs. Histories are saved before
replay so a future nondeterminism failure leaves its reproduction. Never edit
recorded events/results to fix a replay failure: report the code bug instead.
The source HEAD identifies production code; the first committed capture was
made with this new helper uncommitted on c75db0e.

Default offline tests (no server, no model, no activity execution):

```sh
go test ./internal/workflows -run TestRecordedReplay -count=1
go test ./scripts/capture-replay -count=1
```

Coverage tests verify hashes, Update acceptance/completion, both CAN links and
carried receipts/counters, parent/child links, nil child Carry, exact approvals,
and both successful command results. A separate negative control clones a
history in memory, changes one scheduled activity name and requires replay to
fail. It never modifies the recorded fixture.

Limits: this is one successful mock demo path, not real-Ollama proof or coverage
of denial, worker crashes, repair loops, or every future compatibility change.
The four-event successor is intentionally idle/open at capture time, not a
completed workflow. Replaying its startup is not proof of later activity.
