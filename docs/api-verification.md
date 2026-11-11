# Pinned API verification

Pins come from [go.mod](../go.mod), [go.sum](../go.sum), and [mise.toml](../mise.toml), not “latest” documentation. Public signatures below were rechecked in the downloaded pinned module sources during the documentation pass on integrated17 (`c75db0e`). A compiled adapter/test is stronger evidence than merely finding an API name.

## Toolchain and modules

- Go **1.27.3** (module directive and mise).
- Temporal CLI **1.10.0**, explicitly `github:temporalio/cli` in mise. The registry's `temporal` shorthand resolves to the server package, not this CLI.
- Ollama binary and Go module **0.40.15**.
- Temporal Go SDK **v1.50.0**; Temporal API **v1.64.2**; gRPC **v1.83.2**.
- Bubble Tea **v2.1.1** at `charm.land/bubbletea/v2`.
- Bubbles **v2.2.1** at `charm.land/bubbles/v2`.
- Lip Gloss **v2.1.0** at `charm.land/lipgloss/v2`.
- Supporting UI dependencies: `github.com/atotto/clipboard` **v0.1.4**, `github.com/charmbracelet/x/ansi` **v0.11.9**.
- Test assertions: `github.com/stretchr/testify` **v1.12.1**. Transitive pins remain in `go.mod`/`go.sum`.

Local version checks on macOS arm64 returned Go 1.27.3, Temporal CLI 1.10.0 (bundled Server 1.32.2, UI 2.56.1), and Ollama 0.40.15. Ollama's mise install on macOS arm64 was verified in earlier setup work; the [CI workflow](../.github/workflows/ci.yml) validates the Linux installation path. These statements are not a claim that this documentation pass ran Linux CI or installed/pulled another model.

```sh
mise exec -- go version
mise exec -- temporal --version
mise exec -- ollama --version
mise exec -- go list -m all
```

## Temporal: actual APIs and semantics

Pinned source references: [client/client.go](https://github.com/temporalio/sdk-go/blob/v1.50.0/client/client.go), [workflow wrappers](https://github.com/temporalio/sdk-go/blob/v1.50.0/workflow/deterministic_wrappers.go), [workflow API](https://github.com/temporalio/sdk-go/blob/v1.50.0/workflow/workflow.go), [worker API](https://github.com/temporalio/sdk-go/blob/v1.50.0/worker/worker.go).

- `client.Client.NewWithStartWorkflowOperation(options, workflow, args...)` creates the start operation; `UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions)` takes it alongside Update options. A `WorkflowIDConflictPolicy` is required. [Harness client](../internal/client/client.go) uses `USE_EXISTING` for open sessions and `REJECT_DUPLICATE` reuse policy for closed IDs, waits for `WorkflowUpdateStageCompleted`, then calls handle `Get` for the enqueue receipt. Completed Update does not mean completed agent run.
- `workflow.SetUpdateHandlerWithOptions` accepts `workflow.UpdateHandlerOptions{Validator: ...}`. The harness uses validators for message admission and current approval identity; the decision is a recorded Update, not a lossy event/file signal.
- `workflow.NewSemaphore(ctx, int64)` and workflow goroutines/wait groups support [bounded parallelism](../internal/workflows/toolexec.go); native goroutines are activity/client-side only.
- `workflow.AwaitWithTimeout(ctx, duration, condition) (bool, error)` implements the durable approval deadline. `workflow.AllHandlersFinished(ctx)` and `workflow.GetInfo(ctx).GetContinueAsNewSuggested()` support safe rollover in [inbox.go](../internal/workflows/inbox.go).
- [Recorded replay tests](../internal/workflows/replay_test.go) use `worker.NewWorkflowReplayer()` and `ReplayWorkflowHistoryWithOptions`, register both workflows, and supply captured workflow/run IDs in `OriginalExecution` (`workflow.Execution`). This preserves deterministic child IDs rather than using the SDK's default replay ID. Four captured histories and a cloned-history nondeterminism negative control passed; the successor is only an open four-event prefix. See [commands and evidence limits](testing.md#recorded-replay-integrated18), not merely API availability.
- SDK `Worker.Start` initializes capabilities/namespace through internal contexts. The harness's [startup interceptor](../internal/worker/worker.go) binds only startup `GetSystemInfo`/`DescribeNamespace` RPCs to the caller lifetime and becomes a no-op afterward; it does not race asynchronous Start/Stop. Recheck internal call sites and gRPC interceptor ordering on an SDK upgrade.

## Ollama: not an untyped map API

Pinned source: [api/types.go](https://github.com/ollama/ollama/blob/v0.40.15/api/types.go), [api/client.go](https://github.com/ollama/ollama/blob/v0.40.15/api/client.go). Adapter: [internal/llm/ollama/ollama.go](../internal/llm/ollama/ollama.go).

- `api.Client.Chat(ctx, *api.ChatRequest, api.ChatResponseFunc) error` streams through a callback. `ChatRequest.Stream` is `*bool`; the adapter explicitly enables it.
- `api.Message` has `ToolCalls`, `ToolName`, and `ToolCallID`; `api.ToolCall` has an optional ID. Missing response IDs receive stable turn/index fallback IDs in the adapter.
- `ToolCallFunctionArguments` is an ordered-map-backed **struct**, not `map[string]any`; tool schemas use `ToolFunctionParameters` with `*ToolPropertiesMap`. The adapter marshals/unmarshals JSON to these actual types rather than assuming old anonymous schema shapes.
- Stream `EvalCount` is cumulative; the adapter computes nonnegative deltas instead of treating chunks as tokens. A stream ending without `Done` is an error, not a successful empty completion.
- The pinned streaming scanner checks HTTP status while reading a line. The adapter preserves empty-body HTTP failures with a transport/body wrapper and classifies 404 as missing model, 400/422 as invalid request. [Activity mapping](../internal/activities/model.go) makes these nonretryable; provider failure is not concealed by the forced demo plan.
- The provider uses an explicit HTTP(S) host and no cloud credential/provider selection. Default model `gemma4:e4b` has real local-call evidence; `gemma4:26b` remains optional and not hardware-verified here.

## Charm v2: differences from v1 examples

Pinned source: [Bubble Tea model/view API](https://github.com/charmbracelet/bubbletea/blob/v2.1.1/tea.go), [Bubbles viewport](https://github.com/charmbracelet/bubbles/blob/v2.2.1/viewport/viewport.go). Implementation: [TUI model](../internal/tui/model.go), [rendering](../internal/tui/render.go), [modal](../internal/tui/modal.go).

- Import `charm.land/.../v2`, not old `github.com/charmbracelet/bubbletea` v1 examples.
- `tea.Model.View()` returns **`tea.View`**, not `string`. The harness constructs `tea.NewView(content)` and sets view properties (including alternate screen) there.
- Keys arrive as `tea.KeyPressMsg`, with `Keystroke()` for bindings; bracketed paste is `tea.PasteMsg.Content`. The model uses generation-checked commands for clipboard reads and backend I/O.
- `viewport.New()` takes options, not mandatory width/height positional arguments. Use `SetWidth`/`SetHeight`; this version supports `SoftWrap` and `FillHeight`. Textarea dimensions likewise use setters.
- `textarea.CharLimit` alone is not the harness's prompt-memory bound: the harness disables it and enforces an aggregate **16,384 retained runes** across keyboard/paste paths, independent of display-cell width.
- Lip Gloss/ANSI helpers bound and clip terminal output. Approval paging uses the whole available screen and marks truncation; it does not claim the displayed preview is a complete immutable action snapshot.

## Rechecking after dependency changes

```sh
mise exec -- go mod download
mise exec -- go doc go.temporal.io/sdk/client.Client.UpdateWithStartWorkflow
mise exec -- go doc go.temporal.io/sdk/workflow.NewSemaphore
mise exec -- go doc github.com/ollama/ollama/api.ChatRequest
mise exec -- go doc charm.land/bubbletea/v2.Model
mise exec -- go doc charm.land/bubbles/v2/viewport.New
```

Inspect the pinned source behind these APIs, then run [root gates and focused adapter tests](testing.md). Dependency compatibility, real-model behavior, replay determinism, and worker-loss recovery are different claims; record evidence for each rather than inferring all of them from compilation.
