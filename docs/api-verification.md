# Pinned API verification

Verified against downloaded Go module source/godoc during contracts work:

- Temporal Go SDK v1.50.0: `client.Client.UpdateWithStartWorkflow(ctx, client.UpdateWithStartWorkflowOptions)` exists; requires WorkflowIDConflictPolicy and a WithStartWorkflowOperation.
- `workflow.NewSemaphore(ctx, int64)`, `workflow.AwaitWithTimeout(ctx, timeout, condition)`, `UpdateHandlerOptions.Validator`, `WorkflowInfo.GetContinueAsNewSuggested()` and `worker.NewWorkflowReplayer()` exist.
- Temporal API v1.64.2 pinned.
- Ollama v0.40.15: `api.ChatRequest` supports Stream *bool, Messages and Tools. Chat takes a context and streaming callback.
- Bubble Tea stable v2.1.1 uses `charm.land/bubbletea/v2`, not the old v1 module import path. Implementation must verify v2 message/view signatures before using them.
- mise 2026.10.23 registry `temporal` resolves to aqua:temporalio/temporal (server), so explicit `github:temporalio/cli` is required. Verified CLI 1.10.0 starts with `temporal --version` on macOS arm64.
- Ollama 0.40.15 installs successfully through mise's aqua backend on macOS arm64. Linux path remains a CI check.

Read individual source signatures again when implementing each adapter; this record is not a substitute for checking the pinned module.
