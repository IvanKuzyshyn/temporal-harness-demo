# W4 CLI implementation report

## Scope and integration

Base: `7cf93fd`, branch `impl/cli`.

Implemented only the scoped command files and CLI-owned tests:
- `cmd/harness/run.go`
- `cmd/harness/session.go`
- `cmd/harness/wait.go`
- `cmd/harness/cli_helpers.go`
- `cmd/harness/cli_commands_test.go`
- `cmd/harness/cli_regression_test.go`

Main-wiring entry points (exact signatures):
- `runCommand(args []string, out io.Writer) error`
- `sessionCommand(args []string, out io.Writer) error`
- `waitCommand(args []string, out io.Writer) error`

No dispatcher, workflow, approval command, existing interfaces, or existing tests changed. The dispatcher intentionally still rejects these commands until orchestrator wiring. No subagents, push, PR, or merge.

## Behavior

- `run -p` supports session/model overrides, JSONL, explicit auto-approval, external worker, and positive timeout (default five minutes). Default auto-approval is false.
- Default run checks/starts dependencies and embeds workers; cleanup closes only resources owned by the existing managers. External-worker mode uses existing services and does not start dependencies or workers, preserving durability after CLI exit.
- Setup/worker diagnostics and flag help/errors go to stderr. Auto-approval emits a loud warning to stderr and uses the current queried request ID/action hash and OS user; the workflow remains responsible for atomic validation of the decision.
- Cryptographically generated request IDs are supplied to Submit, which uses them as Temporal Update IDs. No shell argument or prompt is executed by CLI code.
- Tail offset is captured before Submit, preserving fast-run events. Reads are bounded by the event package's per-record/per-batch limits; no event history is retained in memory.
- Completion always requires authoritative Query, a run counter beyond the pre-submit baseline plus existing queued inputs, no pending submitted request, and settled state with no queued inputs, active tools, or pending approval. Historical events cannot settle a new request. Child `agent_settled` events are never parent completion signals: terminal event filtering requires `WorkflowID == session ID` AND empty `ParentWorkflowID`. No display event alone can cause success.
- JSON mode emits protocol events followed by a bounded `session_state` summary with ID/status/turn/failure/answer. No banners or raw diagnostics appear on stdout. Child terminal events are omitted, while other child display events can stream.
- Plain mode streams parent message deltas and prints the authoritative final answer separately (it can repeat previously displayed deltas). Missing deltas cannot erase the final answer.
- Failed settlement and timeout return errors. Ctrl-C/SIGTERM detach/cancel the CLI wait, clean up its owned embedded resources, and **do not send Stop** to the durable session.
- New mock sessions using either canonical demo phrase receive `Input.Scenario = "demo"`; existing sessions do not.
- `wait <session> --until settled|blocked --timeout ...` polls Query. Initial idle is not settled without at least one run. Pending undecided approval is blocked. Queued work prevents settlement.
- `session list [--json]` uses existing all-page `Client.Sessions`, then Query per ID. No custom search attributes are required. JSON output is one small summary per line, not an aggregate array. NotFound/FailedPrecondition query results become `closed/unavailable`; other query and listing errors are surfaced.

## Stream loss and deliberate limitations

Display streams are optional, local, and potentially lossy. Corruption/truncation/read errors emit one stderr warning, disable further tail reads, and continue authoritative polling. Missing files simply produce no events. There is no synthetic successful `agent_settled` event: the final `session_state` is the recovery record. If the stream backlog exceeds a bounded read, final state may arrive before every display event is printed; the CLI does not wait for display delivery to settle a durable result.

The pre-submit tail is a cursor initialized before Submit, not a background goroutine; bounded file reads after Submit recover events produced during Submit without a goroutine or unbounded channel.

`Client.Sessions` already collects/deduplicates all IDs across pages, so list inherits that interface's ID-list memory usage; it does not additionally accumulate queried states or output records. Settlement conservatively waits for the entire session's queue to drain, including concurrently submitted work, rather than claiming per-request final answers unavailable in the current state contract.

No live Temporal/Ollama or command-dispatch subprocess integration test was added: dispatcher and later workflow stack positions remain intentionally untouched. Tests call command validation and dependency-injected command cores with fake backends and real temporary event streams.

## Verification

TDD: new tests first failed to compile for the missing helpers, then passed after implementation.

Passed:
- `mise trust`
- `mise exec -- go test ./cmd/harness`
- `mise exec -- go test ./...`
- `mise exec -- go vet ./...`
- `mise exec -- go build ./cmd/harness`
- `mise exec -- go build -o /tmp/w4-harness-cli ./cmd/harness`
- `mise exec -- go test -race -p 1 ./...`
- `git diff --check`

Initial unrestricted `go test -race ./...` hit the existing `internal/devenv/TestReuseAndMock/mock` three-second fake Temporal health timeout under parallel load. All CLI race tests passed. The full race suite passed on rerun with package concurrency limited to one; no out-of-scope change was made.

Coverage includes historical and child settlement rejection, fast settlement capture, JSONL purity, state-authoritative answer/failure, default-off and explicit auto-approval with current identity fields, demo input, corrupted-stream recovery, run/wait timeout, blocked wait, queued-work settlement guard, list statuses/closed workflows/errors, and CLI argument validation without services.
