# Approved chat simplification

Binding scope: user's November 13 follow-up, recorded in controller `simplification-authorized-brief.md`. This is implementation of approved scope, not a new design approval. Base: `1ec7094af12a2397ee3ed4c458c8c0a53d14ba3c`; branch `web-chat/15-simplification`. Sole implementer; controller owns independent reviews and publication. Old Task14 remains held/incomplete.

## Live reference inspected before implementation

Both https://chatbot.ai-sdk.dev/demo/ and the supplied `/demo/chat/ec0fcef8-d48a-4e3b-bab1-b1fc1418ca77` were opened with an isolated agent-browser session. The supplied chat displayed an empty guest interface, not existing private history. No credentials/private content were submitted. Screenshots and DOM metrics are retained in controller `simplification-evidence/`.

Observed at 1440×900: dark monochrome system-sans UI; 48px collapsed rail, 256px expanded sidebar, 56px header when expanded; 864px centered composer, 146px high, 16px bottom gap, rounded corners (~16px), 96px textarea; 32px sidebar controls. Empty heading roughly 28px/semibold, centered above the composer, subdued 14px supporting text. Sidebar history is plain text, not cards. At 390×844: sidebar absent by default, 56px topbar, 8px horizontal composer margins, horizontal prompt suggestions, ~144px composer. No third-party assets/code/fonts copied.

## Product decisions

- Preserve API/SQL/Temporal durable execution, tool consent, stop/replacement semantics, immutable request identity, ownership, checksum and 1MiB upload fences.
- Enabled catalog contains only `qwen3.5:9b`. Keep explicit catalog/profile routing abstractions, exact digest/runtime checks and false global image qualification. Reject disabled Gemma, never substitute Qwen into stored histories. Old SQL rows and installed models remain; incremental NOT VALID constraints enforce new writes without deleting legacy data.
- At most one selected attachment per prompt/replacement. Separate stored blobs remain unlimited by this per-message rule. Reject multi-selection, second/pending upload and stale multi-file drafts visibly; do not slice/drop/upload extras. Arrays remain for extension. Invalid restored selections stay visible/removable, but cannot submit.
- Replace bulky warm-neutral presentation with observed dark reference structure. Remove diagnostic components/render paths entirely, including collapsed Work/Details/context/tool dumps. Keep concise actionable failures, approval/deny/revoke and stop/replacement controls. No inert deploy/auth/share controls.
- New-chat page uses the same compact prompt form; first send creates a durable Qwen conversation and transfers the saved draft to it. Attachment upload starts only once a conversation exists. No semantic model study: zero local inference calls planned.

## Safety and verification

No reset required or authorized by this design. Only this worktree is edited/built; preexisting listeners and Docker resources are inventoried read-only. Unique test-owned databases/ports/browser sessions, retained evidence, bounded commands and serialized Go checks. Tests distinguish deterministic fixtures/real SQL from model inference. Independent review and old full-stack qualification are not certified by this follow-up.
