# Architecture — durable local web chat

Browser → Next.js same-origin proxy → private Go API → Temporal → Go activities → Postgres/Ollama/managed attachments.

## Authority and runtime

Next.js presents history, bounded live drafts, tool approvals, attachments and queued/replacement controls. It never writes application SQL or runs a second agent loop. The loopback API rejects unexpected Host/Origin/fetch context and requires a server-only proxy secret. Every command uses a retained request ID and payload digest; SQL transactionally stores durable intent before asynchronous Temporal dispatch. Acceptance is not completion.

`WebChatSessionWorkflow` owns execution, policy, cancellation/join and bounded Continue-As-New state. Durable command receipts survive retries/rollover. No filesystem/network/database I/O occurs in workflow code. Activities own I/O and idempotent canonical result writes. Completed logical results are not duplicated by overlapping attempts; incomplete draft events are not durable answers.

`cmd/web-worker` registers three queues: `web-chat-agents` (session/SQL/tool work), `web-chat-llm` (model steps), `web-chat-documents` (document child workflow/chunks/image work). Actual pollers for workflow and activity queues are checked by API readiness. Default one model generation per runner, four document activities, twelve agent steps, fifteen-minute document child limit. Poller readiness is an observation (recent pollers can be stale), not a lease.

## Four retained Temporal patterns

- Long-running streaming model activities heartbeat and honor cancellation; retries have distinct attempt identity and durable cleanup records.
- Approved safe fetches and bounded document chunks run in parallel; model calls remain serialized per runner.
- `DocumentAnalysisWorkflow` is a real child with immutable parent-authorized attachment/instruction/budget scope. No arbitrary child tools or approval bypass.
- Recorded, action-bound approval: once, conversation/version-scoped all, deny/expiry/revoke. Upload is not consent. Replacement joins owned work before it starts, ahead of queued follow-ups.

See [execution](web-chat-temporal.md), [analysis](web-chat-analysis.md), [approvals](web-chat-approvals.md), [context](web-chat-context.md) and [streaming](web-chat-streaming.md).

## Storage and containment

Postgres has separate application/Temporal/visibility databases. Original/derived attachment bytes reside in one persistent absolute host directory shared by API and workers; SQL holds ownership/metadata/extracted text. Hard-link publication and atomic metadata are required. Recovery does not delete unknown metadata or infer ownership from a loose prefix. The pinned stdin-only parser runs without network/mounts, read-only/nonroot with CPU/memory/PID limits; uncertain cleanup fails closed.

Only `web_fetch` and `file_upload` are registered. Fetch rejects private/reserved destinations and validates DNS/redirects; external content and filenames are untrusted data. There are no coding tools, shell execution, project discovery, fixture forcing or repair loops. Context admission preserves provenance and enforces bounds; exact models never silently fall back. [Security and parser details](web-chat-attachments.md).

## Local lifecycle

Public `mise run setup|up|down` uses `scripts/dev/` and a private runtime home. Setup builds a new immutable source/output snapshot, checks exact helper/model identity and leaves no newly owned service. Up migrates and verifies the background stack and current served CSS. Down closes API streams, Next and workers then verified Docker resources, retaining volumes/files/models and never cancelling workflows. OS locks, pre-acquisition journals and kernel process identities protect concurrency, rollback, stale/reused PIDs and controller death. [Ownership/backup contract](web-chat-operations.md).

Local client closure, OS process exit, remote TCP ConnEnd and provider/GPU reclamation are different evidence. The intermittent ConnEnd observer test remains uncertain; lifecycle does not claim shared GPU unloading. Known larger-image Qwen and narrow parent semantic failures remain explicit deferred limitations, not safety waivers.

## Retirement boundary

Terminal UI, CLI/automatic desktop ownership, sample project, local tools, filesystem transcript/JSONL transport and legacy workflows are removed. Shared heartbeat/join, startup-RPC scope and UTF-8 bounds remain in web runtime and tests. New web integration tests exercise the four patterns and restart/approval/child/cancellation boundaries. Old captured histories in `docs/historical/terminal-replay/` are historical only—not replay compatibility evidence for `WebChatSessionWorkflow`. No legacy workflow/data migration is promised; user artifacts are untouched.
