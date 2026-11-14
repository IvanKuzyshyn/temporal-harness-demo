# Retained narrow-image semantic evidence (immutable copies)

These are actual Task12 captures from the exact 640×160 `scripts/testdata/bonjour.png` profile, not generated fixtures or a new qualification run. Correct English translation is `Hello world`. The parent user instruction is unchanged. The Qwen failure remains R2 **OPEN**. No arbitrary-image qualification is implied.

Canonical evidence root: `web-chat-00-design/.superpowers/sdd/2026-11-11-web-chat/task-12-evidence/` (sibling controller worktree, outside this source commit).

- `qwen-narrow-parent-failure.json`: `verified/agent-qwen-native-history.json`; SHA256 `9000e6e3137938d41a3a86d4ade6f5f901cb843471eecc0e06cf65c2dde6e637`. Actual saved messages and rendered tool JSON. Completed parent contains `Hello World.`, stray `</think>`, then contradictory inability to provide the translation. This MUST FAIL full-answer acceptance.
- `qwen-narrow-parent-conforming-dom.json`: `agent-qwen-image-result.json`; SHA256 `5b94ee70427fef397f19a47100d85edf881cc835d02987244d293fb22e48f916`. Earlier **different conversation**, DOM-only capture, `HELLO WORLD`. No SQL/provider record is invented for this sample. Passing it cannot resolve the retained later failure.
- `gemma-narrow-parent-conforming.json`: `handoff/live-first-image.json`; SHA256 `9379db8eb825e5138f40c95228000d0e615bca649838493786b808f0b63bda94`. Actual completed history and tool rendering; final `Hello world.`.

`web/test-support/image-acceptance.test.ts` rejects the retained failure and accepts the conforming samples using the same whole-final assertion used by the live test. A green negative regression means the validator detected a known defect, not that Qwen is qualified. The phrase-specific rule is test-only; there is no production text rewriting/filtering.

`internal/activities/web_parent_diagnostic_test.go` reconstructs the Qwen parent request from the retained failure plus current unchanged application source. Its HTTP responses and parser token streams are explicitly **synthetic boundary fixtures**, never original provider captures. No real provider is contacted. Source-derived request accounting matches the retained accounting, but original NDJSON/generation tokens were not retained, so the provider generation/parser causal split remains unresolved.
