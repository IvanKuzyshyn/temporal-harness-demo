# Local project assistant and terminal journal

## Intent and authorization
The user approved the chronological terminal mockup and launch-directory behavior, confirmed local workers, and explicitly requested implementation without further approval pauses, with new PRs based on draft #18. Preserve all existing drafts and main. This is an improvement to the Go terminal application, not a browser app.

## Required behavior
1. Normal `tui` and `run` use the invocation's current directory unless HARNESS_WORKDIR or an explicit workdir option overrides it. Mise must not silently select the calculator fixture. The absolute effective project root and session model must be visible. A reconnected session keeps its original configuration; mismatches must be explicit, not falsely presented as a reconfiguration.
2. The guided fixture is opt-in. `/demo` remains labeled harness-enforced and must not mutate an arbitrary project. Demo usage documents selecting a disposable sample copy; default normal startup never selects the fixture.
3. For requests such as 'explain this project', actually inspect files in the selected project. Supply bounded fresh discovery, relevant manifests and representative source content, not just old README claims. Provide a coding-agent instruction that requires evidence-backed answers, file references, actual inspection where needed, honest failures and test claims. Distinguish normal model-selected tools from automatic context reads and the forced demo. No guarantees of universal model reliability.
4. Repository content is data, not privileged system instructions. Refresh context for new logical runs; retries of one logical run remain idempotent. Prefer current evidence over stale snapshots. Automatic discovery skips hidden/sensitive and generated/dependency directories; remains bounded and inside project root. Do not recursively ingest everything. All filesystem access remains outside workflows.
5. Durable transcript history is the source for completed messages/tool results; optional JSONL events enrich live progress but cannot erase history. Load older history on demand with bounded pages. Reconnect, periodic snapshots, retries and session switches do not duplicate/drop completed messages or mismatch roots. Tool outcomes remain paired and distinct across logical runs.
6. Terminal UI: one chronological journal including user, assistant, file/tool activity and child summaries. Expand/select tool details and scroll command output beyond two lines. Clearly label running/succeeded/failed/denied/interrupted/awaiting approval outcomes with text and color. Never infer verification from prose. Show real phase/current active tools and elapsed/freshness indicators, not fake percent-complete. Unknown heartbeat freshness stays unknown.
7. Keep the prompt pinned and visible at supported normal sizes (80x24 and larger), with history taking the remaining height. Support keyboard scrolling, mouse wheel, stable scrollback while new data arrives, unread count and jump-to-latest. Key help discoverable; preserve Enter/send, Esc/Stop, Ctrl+C/detach and exact approval binding. Tiny sizes clip safely and say resize rather than hiding a consequential approval decision.
8. Local worker/filesystem scope only. No remote filesystem transport, distributed workspace synchronization, cloud provider, dependency upgrades or OS-sandbox claims.

## Safety and compatibility
Keep pinned tool/dependency versions. Unsafe tool execution still requires approval and single-attempt conservative recovery. Read-only inspection must not execute repository code. Preserve old recorded history replay with Temporal GetVersion where command sequences change; do not replace fixtures to hide nondeterminism. Bound all history pages/context/output; sanitize terminal escape sequences. Do not stop default services or unrelated workers in verification. Keep the user's primary checkout and existing sample edits untouched.

## Acceptance
- Deterministic tests for CWD/env/flag precedence and actual session config; temporary unrelated projects with distinct sentinel contents prove roots are not silently swapped.
- Tests inspect actual model requests and real filesystem reads, including changed files between logical runs, inaccessible/missing context, excluded content and prompt-injection-shaped repository text.
- Existing captured histories replay unchanged. Private-server mock durability suites still pass.
- Journal tests prove completed history survives snapshots/reconnect, paging and duplication boundaries, command detail expansion, unsafe refs and stale async responses.
- Render/input tests at 80x24, 120x40 and narrow/tiny sizes. Prompt visible, scroll position stable, new-event count, jump-latest, mouse scroll, status semantics, approval accessibility.
- Real local Ollama smoke on fresh temporary projects without forced demo, plus actual PTY interactions on final tree. Report model limitations honestly.
- Independent task/final review, serialized build/vet/unit/race and green CI for every new draft before stacking the next.

## Design decisions made by implementer
Retain Bubble Tea/Bubbles/Lip Gloss rather than web UI. Use a bounded local artifact reader for history (local-worker milestone), not giant workflow Query payloads. Use known work counters only when a denominator exists; model generation is indeterminate. Keep demo selection explicit rather than auto-copying a fixture on ordinary startup. Browser mockup is an appearance/interaction reference, not evidence of real executions.
