# Historical: evidence-grounded project requests

> Project discovery, local coding tools and fixture forcing are retired. This document is historical evidence only, not a current runtime or replay contract.

In new sessions, ordinary `run`/`tui` requests use the session's effective local project root and model. They do not select the calculator fixture or the harness-enforced `/demo` plan. Old executions retain their versioned demo-routing behavior until rollover or a new execution. “Explain this project” is an ordinary model request, not a script. Existing sessions retain their original configuration; reconnecting does not change their root or model.

## Automatic inspection versus tools

Before the model's first turn of each new logical run, an activity gathers a fresh **evidence snapshot**. It lists observed paths and reads recognized manifests, likely entry points, README files, then a general-source fallback. Supported candidates include Go, JavaScript/TypeScript, Python, Rust, Ruby, Java, C/C++, C#, and others. These are selection hints only: facts come from observed file contents, not guessed project classifications.

Automatic inspection:

- visits at most **4096 directory entries**, counting excluded entries too;
- attempts at most **8 representative file reads**, including manifests and README;
- reads at most **8 KiB per file** and **32 KiB total file bytes**;
- emits at most **32 KiB of aggregate evidence**, including paths, labels and diagnostics (the stored envelope also fits this bound);
- bounds the path listing to 4 KiB and discovery diagnostics to 2 KiB;
- skips hidden entries and `node_modules`, `vendor`, `dist`, `build`, `target`, `__pycache__`, `coverage`, and `venv`;
- follows no discovered symlinks, opens through a pinned `os.Root`, and rejects escaping links even if paths change between discovery and reads;
- reports truncation, skipped symlinks, read failures and missing evidence rather than claiming complete access;
- **never executes project code, commands, tests, manifests or scripts**.

TODO text is included only when present in the selected files. Fresh snapshots do not run a separate repository-wide TODO scan or Git status. The legacy Git/tree/README/TODO activity schedule remains available solely for old-history compatibility. Legacy recursive tree/TODO inspection also excludes hidden and generated/dependency entries; automatic README reads reject symlinks.

Model-selected inspection tools remain available when this sample is insufficient. Explicit `read_file` is a separate guarded operation and can read some paths omitted by automatic discovery. Unsafe operations still require the existing approval mechanism. This is a local-worker scope guarantee, **not an OS sandbox**, remote filesystem transport, or a guarantee against every form of sensitive content in ordinary visible source files.

## Request preparation and trust

`agentMessages(messages []llm.Message, workdir string) []llm.Message` is pure request preparation. It prepends a trusted coding-assistant policy and quotes the effective project root supplied by `LLMInput.Workdir`. The policy requires answering current user intent (or asking for clarification), inspecting relevant source when needed, citing inspected paths, and reporting uncertainty or unavailable access.

Repository snapshots and explorer initial file context are JSON-quoted **DATA** in non-system messages, not privileged instructions. Data is placed before the current user request so an appended snapshot does not become the apparent question. The latest evidence snapshot supersedes older automatic snapshots in provider requests. Earlier assistant/tool conversation remains paired and ordered; historical claims in it may be stale and the policy explicitly prefers fresh evidence. JSON quoting can expand the wire representation beyond the raw 32 KiB evidence budget.

Explorer read-only instructions are retained, including when the child has no ordinary user message. Its four-tool allowlist and workflow enforcement remain unchanged. The parent policy does not authorize an explorer to modify files, run commands or delegate.

Inspection is not verification. A README saying “tests pass,” a source test function, or an assistant plan is not evidence that a command ran. Only actual command outcomes establish execution, including failure and interruption status. Prompt policy and quoting reduce ambiguity; they do **not** guarantee that every model follows instructions or resists every prompt injection.

## Retry and replay compatibility

- `GatherInput.InputID string` scopes first-writer snapshot keys to a logical run. Retries reuse the committed snapshot even if source changes; later logical runs get distinct snapshots.
- Empty `InputID` preserves legacy `context/<kind>` keys, including their stored `system` role suffix. Stored roles are normalized only in outgoing requests to avoid duplicate persisted entries.
- `workflow.GetVersion("project-context-refresh/<RunCounter>", DefaultVersion, 1)` retains original scheduling for replayed runs. The next run of an upgraded open or continued session can select the fresh branch even when `ContextGathered` is already true. This is intentionally not one permanent session-wide version choice.
- All filesystem I/O and request preparation occur in activities. Workflow history stores bounded references; durable snapshots and conversation are not rewritten. Existing Continue-As-New/history bounds and demo versioning are unchanged.

## Evidence and limits of verification

Unit tests capture real `LLMStream` requests from temporary unrelated Go and JavaScript projects (including the [weather fixture](../internal/activities/testdata/weather/)). They cover manifest and implementation contents, distinct roots, malicious README text, changed files across logical runs, stable retries, missing/denied access, exclusions, fallback discovery, byte/file/entry limits, explorer constraints and assistant/tool ordering. Workflow tests cover legacy/fresh/upgrade version choices and retry input keys. Original captured histories are replayed without changing their bytes or manifest.

These tests prove filesystem selection and request construction, **not live model answer quality**.

A separate ordinary-mode smoke passed on 2026-11-10 using local `gemma4:e4b`, Ollama 0.40.15, and an owned private Temporal server on macOS/arm64:

- Launching from unrelated Go and JavaScript directories, without a workdir flag or environment override, produced the correct absolute roots, source-file citations, and exact values present only in implementation files.
- An explicit request to inspect a text file outside the automatic source sample produced an actual persisted `read_file` result and the correct identifier.
- Editing the Go source between prompts in the same session produced the new value, with another actual `read_file` call rather than the old answer.
- The worker was deliberately bootstrapped with `--model mock`; authoritative requests still used the real original model, exercising request-based provider dispatch on shared queues.

All four prompts used ordinary mode, without the harness-enforced demo or automatic approval. Fixture contents remained unchanged by the harness, and the probe terminated and joined its own workflows/processes without restarting the existing Ollama or default Temporal services. These controlled observations are not a statistical model-quality benchmark, a general upgrade guarantee, or proof that every model resists prompt injection. Terminal interaction and full-stack durability have separate qualification gates.
