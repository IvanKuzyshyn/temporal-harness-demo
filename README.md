# Temporal Agent Harness

A local Go + Bubble Tea + Ollama teaching harness for four Temporal patterns: long-running activities, parallel tool execution, child workflows, and recorded human approval. Temporal owns session state; activities do I/O; streamed text is a display aid, not proof of success.

Implemented here: durable sessions, CLI/TUI, guarded tools, approval, a fixture-only guided demo, and approval-gated post-write verification with at most two automatic repair runs. Live mock and local Gemma CLI demos passed all four patterns; a mock TUI keyboard smoke and recorded-history replay also passed. These are scoped proofs, not native model action-selection evidence. Reviewed isolated worker-loss, approval-restart and Stop/Continue tests also pass with mock; they explicitly demonstrate that hard worker loss can leave an orphan requiring separate cleanup. See [testing and evidence](docs/testing.md) and the [original implementation plan](docs/superpowers/plans/2026-11-09-harness.md).

## Quickstart

Target platforms: macOS arm64 and Linux amd64. Install [mise](https://mise.jdx.dev), then run from this repository:

```sh
mise trust
mise install
mise run setup
./bin/harness doctor
```

`setup` installs pinned tools, downloads Go modules, builds `bin/harness`, and runs `doctor --fix`. That starts missing local dependencies and pulls the model; downloads can be several GB. Successful standalone setup intentionally leaves its services running. No cloud API keys are required.

Use a **temporary copy** of the teaching project before any scripted run or auto-approval. Its committed `calc/calc.go` deliberately subtracts instead of adding:

```sh
sample=$(mktemp -d "${TMPDIR:-/tmp}/harness-sample.XXXXXX")
cp -R examples/sample-project/. "$sample/"
export HARNESS_WORKDIR="$sample"
./bin/harness demo --workdir "$sample"
```

`demo` opens a guided banner, but does not autoplay. Enter `/demo` to start; review each approval. Alternatively:

```sh
./bin/harness demo --autoplay   # starts the scenario; approvals are still manual
./bin/harness tui               # free chat, no scripted startup
./bin/harness tui --session SESSION_ID
```

The guided scenario makes actual provider calls, then **replaces the parent's action plan** with a labeled, harness-enforced fixture script: parallel reads, a read-only explorer child, an approved correction, an approved test, and a result-based summary. It is not evidence that the model independently chose those tools. Provider failures remain failures; unrelated fixture contents are refused. A successful write also triggers a **separate afterRun test approval**, even if the scripted test already ran. There is no blanket implicit approval.

## Models and configuration

- Default: local Ollama `gemma4:e4b`, verified in a live all-four-pattern CLI demo with actual provider calls, three approvals, and successful scripted plus separate afterRun tests. The parent action plan was harness-enforced, not independently model-selected.
- Optional larger model: `HARNESS_MODEL=gemma4:26b ./bin/harness doctor --fix`, then `./bin/harness tui --model gemma4:26b`. This model is **not hardware-verified here**.
- Deterministic development: `HARNESS_MODEL=mock ./bin/harness tui`. Mock skips Ollama, but a live session still needs Temporal. Unit tests use local fixtures/test environments and do not need a live server.
- A supported `--model` flag overrides `HARNESS_MODEL`; a nonempty environment value overrides code defaults. Mise preserves caller overrides. Original model/workdir and other workflow input remain fixed for an existing session; later client flags do not reconfigure it. Use a fresh ID after a closed execution or when changing configuration.
- Project root for `run`, `tui`, and `demo`: nonempty `--workdir` > nonempty `HARNESS_WORKDIR` > **invocation current directory**, resolved to an absolute path. `mise exec --` preserves that default; mise no longer selects the fixture. Mise tasks themselves run in their task directory, so use `mise exec -- /absolute/path/to/bin/harness tui` from another project (or pass `--workdir`). Nothing is automatically copied or created in the project.
- The TUI shows the authoritative original session model/root and warns when client selection differs. `run` prints sanitized original configuration and mismatch diagnostics to stderr, before submission on reconnect or at the first query for a new session. Older workers without metadata are explicitly unknown, not assumed to use the reconnecting client's selection. Use a new session to change configuration. `run --json` keeps stdout JSONL-only and includes `session` metadata in its final `session_state` when available.
- Workers dispatch each activity request by its original model: exactly `mock` uses mock; every other value uses the configured local Ollama endpoint and that exact model, with no fallback. This also applies to shared queues and reconnects with different client selections. Mock-only processing constructs no Ollama client; a real request still requires the configured endpoint/model to be available.
- New workflow runs select the guided script only via an explicit `/demo` message or explicit initial `Scenario=demo`; ordinary natural-language fixture prompts do not opt in, even with mock. Old recorded workflow histories retain their natural-language alias behavior through a Temporal version gate.
- Defaults: Temporal `localhost:7233`, UI `localhost:8233`, Ollama `127.0.0.1:11434`, workdir current directory, artifacts `.data/`. See [all settings and service ownership](docs/operations.md).

## Terminal journal

The native TUI reserves bottom rows for your draft at 80×24 and 120×40. PgUp/PgDn
or the mouse wheel browse without following new output; **End** returns live.
Cyan **You** and green **Assistant · response completed** headings distinguish
roles; completion does not certify tests. Intermediate work starts collapsed;
**Ctrl+T** expands one group, while concise failures and approvals stay visible.
Ctrl+Up/Down selects an entry, Ctrl+O opens raw detail, and Ctrl+L opens a captured
command log (Ctrl+Left/Right pages beyond the model tail). `/help`, `/detail N`,
`/lognext`, `/older`, `/latest`, and `/metadata` provide discoverable fallbacks.
Original model/project, actual per-run mode, real phase/timing, active counts and
observed heartbeat freshness are labeled honestly; read evidence is not test
verification. Approval preempts drafting; below 40×12 decision dispatch is blocked
with a resize warning. See [native terminal behavior and qualification limits](docs/terminal-journal.md)
and [controlled real-model, durability and PTY qualification](docs/project-assistant-qualification.md).
The [conversation-first preview and rebuild instructions](docs/conversation-first.md)
show actual native terminal captures for the readability correction.

Fresh ordinary runs gather **one bounded project-evidence activity**, not the old
four parallel context gatherers. Parallel tool batches and the explicit guided
demo still demonstrate parallel execution; old replay histories keep their schedule.

## Noninteractive example

Using the temporary copy above, this **explicitly risky** option approves every current tool request observed while the command waits, including the separate verification request:

```sh
HARNESS_MODEL=mock ./bin/harness run \
  -p "Find what is broken in the sample project and verify the fix" \
  --json --auto-approve --timeout 10m
```

Omit `--auto-approve` for manual decisions via the TUI or `approve`/`deny`. JSONL stdout contains display events followed, on observed settlement, by an authoritative `session_state` summary. Warnings/errors go to stderr. Timeout or SIGINT detaches without sending Stop; Esc in the TUI sends Stop. Service/worker ownership still matters for continued progress. See [CLI contracts](docs/operations.md#cli-contracts).

## Development

```sh
HARNESS_MODEL=mock mise exec -- go build ./...
HARNESS_MODEL=mock mise run lint
HARNESS_MODEL=mock mise run test
HARNESS_MODEL=mock mise exec -- go test -race ./...
```

The nested sample module is intentionally excluded from root `go test ./...`; `(cd examples/sample-project && mise exec -- go test ./...)` **fails before the fix**. Do not “repair” the committed baseline as part of running harness tests. Prefer deleting your temporary copy after all sessions using it are stopped; if you edited the repository copy, inspect and back up its diff before restoring only that file, never blanket-reset the repository.

Pins: Go **1.27.3**, Temporal CLI **1.10.0**, Ollama **0.40.15**. Exact SDK/Charm pins and API differences are in [API verification](docs/api-verification.md).

## Read next

- [Architecture, Mermaid diagrams, and source map](docs/architecture.md)
- [Operations: commands, TUI keys, isolation, troubleshooting](docs/operations.md)
- [Testing and evidence boundaries](docs/testing.md)
- [Draft PR stack inventory through intended position 22 (CSV)](docs/pr-stack.csv) — bases, scope, validation focus, and live check links
- [Event protocol](docs/events.md)

## Trust boundary

Use only trusted projects and local services. Guarded paths, argv allowlists, and approval are **not a sandbox** for arbitrary Go tests/builds or Git/project code. Approval identity is local display metadata, not authentication; previews are bounded observations, not immutable filesystem snapshots. Hard worker SIGKILL can leave orphan command process groups. Local durability requires both the Temporal database and artifact storage; this is not a multi-host shared-storage or workflow-code-upgrade guarantee. See [limitations](docs/operations.md#security-and-durability-limits) before experimenting.
