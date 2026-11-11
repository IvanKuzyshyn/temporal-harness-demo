# Project-assistant qualification

The follow-up stack adds launch-directory workspaces, authoritative original session configuration, fresh bounded file evidence, durable paged history, and the native terminal journal. These notes describe controlled verification, not guarantees about every model response or terminal.

## Functional and compatibility checks

- Serialized build, vet, uncached unit/race suites, module-diff, formatting and whitespace checks passed. The original recorded replay fixtures were not replaced. Context scheduling and explicit-demo compatibility remain versioned; UI progress fields do not schedule work.
- Private real Temporal/mock durability tests passed normally and under the race detector: approval survival across worker/client restart, exact stale/hash/duplicate rejection, single-attempt unsafe recovery, actual Stop/process-group join, and retained FIFO/Continue. Hard worker loss does not itself kill an orphan command; the test separately cleans its owned group and does not credit that cleanup to recovery.
- Existing session root/model/data-directory/scenario stay authoritative. A reconnecting client's selections do not reconfigure execution. CLI streaming selects the original directory before Submit; known invalid original storage disables only optional streaming, without falling back to unrelated client storage. Unknown legacy configuration and its client-selected optional-stream fallback are explicitly labeled.

## Real local model

Using existing local Ollama 0.40.15 with `gemma4:e4b`, ordinary unforced requests on unrelated Go and JavaScript projects identified the respective projects, cited source files and returned source-only sentinels. Session metadata retained each original root. An explicit file request produced a persisted native `read_file` result; changing source in the same session produced the new value. A shared worker bootstrapped with mock still honored the original real model.

The four-command sequence observed cumulative native reads 0/0/1/2: the first two answers used real automatic filesystem evidence, not claimed model-selected tools. A separate actual TUI request answered a source-only question with a pinned unsent multiline draft. No ordinary probe entered the forced demo, approved a command, or mutated its project.

These are representative controlled probes, not universal model reliability or prompt-injection resistance. Repository data is separated from trusted policy, but that is not a proof that every model obeys it.

## Actual terminal interaction

The final binary was driven through a real PTY, not the design mockup:

- Reconnected to a 77-record session after deleting its owned optional display JSONL, from a different viewer directory/data selection. Original history remained available. Older/latest paging, incoming-update notifications, stable reading, long-record 80→120→80 reflow, mouse scrolling and pattern-view round trips were exercised.
- Unsent drafts stayed pinned at 80×24 and 120×40, including a two-line draft during an ordinary real-model request. End returned to live history without discarding the draft.
- An explicitly requested guided demo on a disposable sample copy used three real keyboard approvals, changed the intended file, ran real tests and completed a real child workflow. All four orchestration patterns were observed; an independent `go test ./...` also passed.
- At 40×8, approval and denial keystrokes were refused without changing the bound request/hash or executing the project. A typed denial reason survived resize. Normal-size denial did not create the command's execution marker.
- Ctrl+C detached while an approval remained pending and the workflow remained running. It was not treated as Stop.
- An approved deliberately failing command produced 1,000 real log lines and exit 1. TUI detail showed failure; full-log paging displayed a middle sentinel absent from the model's bounded tail. Draft and log scroll position survived a pattern-view visit.

Tool-ID reuse and malformed outcome/identity cases are covered by deterministic reader/controller regressions. The mock's single-tool scenario is transcript-wide one-shot; live PTY history is not falsely described as repeated native tool calls.

The observer retained raw ANSI, decoded cells and text. Its pinned pyte implementation required explicit CSI S/T scrolling-region support; this was validated with a failing pure-stream test and replay of the exact captured ANSI. Reference PNGs were rendered from those native cells, not invented HTML content or OS-terminal screenshots. Independent same-provider visual evaluation passed; only OpenAI was configured, so no cross-provider claim is made.

## Bounds and ownership

See [terminal controls and limits](terminal-journal.md), [durable history](durable-history.md) and [grounding](project-grounding.md). History/cache/render/log limits are explicit; unseen counts measure updates, not unique messages. Log byte paging is bounded and not snapshot isolation across external edits. Captured read evidence and assistant prose do not certify command success.

Verification used private nondefault-loopback Temporal servers, temporary projects and owned workers. Owned workflows/processes were terminated/joined and temporary projects removed; default Temporal/Ollama services and unrelated workers were not restarted. Existing primary-checkout edits were left untouched. Commands execute approved project code, not sandboxed code.

Final draft status and current checks are linked from [the stack inventory](pr-stack.csv). All PRs remain drafts for manual review; none are merged by this work.
