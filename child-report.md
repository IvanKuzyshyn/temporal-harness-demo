## Child contract/integration follow-up

- Scope: `internal/workflows/explorer.go`, `internal/workflows/explorer_test.go`, and this report only.
- Child started/completed events now use `events.ChildData` with direct child workflow ID, bounded task, and completion summary reference. Delegate result details retain `childWorkflowId`.
- Explorer overrides the inherited mock scenario with `explorer`; tests explicitly pass parent scenario `demo` and verify the child model input.
- Added child-scoped agent, turn, and tool lifecycle events, including initial-context and rejected tools. Terminal events execute on a disconnected context; failure/cancellation settlements report failed, not done. Tool event arguments and summaries are bounded.
- Model, tool, and transcript activities use `WaitForCancellation` and disconnected future waits. Delegation likewise waits for child cancellation and cleanup before emitting completion. Cancellation stops further tool/context processing.
- Tests cover direct event payload fields, retained delegate details, child scope, balanced lifecycle pairs, success/failure settlement, and delayed child workflow cancellation cleanup.
- Validation passed: `go test ./...`, `go vet ./...`, `go build ./...`, `go test -race ./...`.
- Validation limitation: the SDK unit test environment synthesizes activity cancellation completion immediately; a delayed real-activity cleanup assertion is not representative there and was not retained. Real-server activity cancellation timing was not exercised. Child workflow delayed cleanup is covered by a passing regression test.
- CLI filtering of child settlement remains outside this scope. No subagents, push, PR, or merge performed.
