# Local web-chat operations

[Quickstart](../README.md) · [Architecture](architecture.md) · [Tests/CI](testing.md)

## Prerequisites and public commands

Install mise and Docker/Compose yourself, trust this checkout (`mise trust`), and start the Docker daemon. `mise run setup` installs the pinned tools from `mise.toml`, including Python3.12.16 for the standard-library lifecycle driver. No Python packages are required. macOS/Linux process identity backends are supported. Complete model/image qualification currently requires the recorded Ollama0.40.15 MLX/safetensors runtime and exact model digests; other runners need explicit requalification, not an automatic fallback.

```sh
mise run setup  # pinned dependencies, frozen pnpm, parser + fresh app build, enabled exact Qwen model
mise run up     # background services; migrations, models, helper and health gates
mise run down   # stop owned services only, preserve all data
```

Large model downloads are announced before pulling missing exact tags. Existing model tags are checked, not silently overwritten. Setup reuses a compatible existing Ollama endpoint without taking ownership; an Ollama it starts is stopped on success/failure. Setup refuses to rebuild a running owned API/worker/web stack: down first. Failed setup keeps the previous successful build pointer. Failed startup retains actionable logs; rollback stops only resources not running before the invocation. No command submits a workflow cancellation.

### Configuration and isolation

First invocation creates `.webchat/config.json` with independent random database/proxy secrets, a random Compose project/owner identity, canonical absolute paths and loopback endpoints. Files are mode0600; runtime directory is mode0700. Existing values are retained on rerun; `.env` is not sourced/overwritten. Do not copy ownership receipts to another checkout, change database passwords without SQL coordination, or edit a running stack's paths/ports. `.env.example` documents manual internal configuration only.

For an independent instance, choose **unused** ports and a fresh absolute home before the first setup (subsequent commands need only the same `WEBCHAT_HOME`):

```sh
export WEBCHAT_HOME="$PWD/.webchat-lab"
export WEBCHAT_POSTGRES_PORT=25432 WEBCHAT_TEMPORAL_PORT=27233
export WEBCHAT_TEMPORAL_UI_PORT=28080 WEBCHAT_API_PORT=28081 WEBCHAT_WEB_PORT=23000
# Shared compatible Ollama default:127.0.0.1:11434. To own an isolated endpoint:
# export WEBCHAT_OLLAMA_PORT=21434
mise run setup
mise run up
mise run down
```

Default ports: Postgres15432, Temporal17233, Temporal UI18080, private API18081, browser13000, Ollama11434. Collisions fail; never kill by port. Do not use a second home pointed at the first home's database, attachments or volumes. Existing Ollama must have the exact compatible runtime and enabled Qwen model (Gemma is not required even for previously created lifecycle homes); an incompatible listening endpoint is never killed/reconfigured. Model downloads remain in Ollama's own configured model directory (normally `~/.ollama/models`) and are never removed by lifecycle commands.

## Persistent resources and ownership

- SQL: Docker volume `<project>_postgres-data`, holding application `webchat`, Temporal `temporal` and visibility `temporal_visibility` databases. Roles/passwords are separate. Project name is printed by up and stored in config.
- Originals, derived rasters and recovery metadata: **`<WEBCHAT_HOME>/attachments`**. The exact same persistent absolute `WEBCHAT_ATTACHMENT_ROOT` is exported to host API and workers. No unused Docker attachment volume or ephemeral default substitutes for it.
- Configuration/receipts/journal/logs: `<WEBCHAT_HOME>`. Binaries/private frontend source snapshots and outputs: `<WEBCHAT_HOME>/builds/<random-id>`. Old builds/logs/receipts are preserved, not automatically pruned.
- Legacy `.data/`, old transcripts, models and unrelated Docker resources are never touched.

A kernel advisory lock serializes commands. A journal is synced **before** acquisition and records the previously-running set and planned resources. Commands operating on Docker inherit the lock, so controller death cannot let a second command race an unfinished Docker RPC. Next invocation reconciles an interrupted journal first. Rollback failure retains the journal for retry/inspection.

Host service supervisors publish receipts before starting children and join children on TERM. Ownership uses kernel birth identity (macOS creation microseconds; Linux boot ID/start ticks), uid/group and exact recorded PID—not a port or PID alone. This remains valid when Node changes its process title. A dead supervisor's child is stopped only with a matching creation identity. An ambiguous/stale identity is never signaled. Docker objects require exact project, service and random ownership label; unexpected or ambiguous matches fail closed. Previously owned running services survive another invocation's failed startup. Down stops containers by their verified IDs; it does not remove containers/networks/volumes. Transient parser containers are owned by exact activity-created identities, not the lifecycle project.

API shuts down before Next so open SSE does not prevent Next's graceful drain. Worker Stop joins activities within SDK bounds; transport shutdown, process exit, remote observer and provider/GPU reclamation are distinct. Down certifies verified local process/container exit, **not** GPU/model unloading on shared Ollama. The retained intermittent `TestRuntimeCancellationPreservesShutdownRPCs` ConnEnd observer failure is not declared fixed; assertions and 3s boundary remain.

## Readiness and fresh frontend builds

Up checks Docker daemon/Compose, port ownership, shared storage hard-link publication, exact model/runtime identity, parser image identity and execution, Postgres/Temporal/UI health, migrations, actual worker polling and private API readiness. It fetches the browser page and compares served CSS bytes with the successful fresh build's assets. A build exit0 alone is not accepted as proof of current assets.

Setup copies frontend source into a **new private build directory**, installs frozen dependencies into that build's own `node_modules`, and runs Next's production webpack builder there. Neither source/output nor runtime module links point at a mutable checkout dependency tree (only pnpm's immutable content-addressed store is shared). Next's generated `tsconfig`/`next-env` writes and all caches stay in that copy. This avoids changing an active/shared `.next`, deleting arbitrary cache paths, or the observed stale incremental Turbopack CSS path. It is a bounded fresh-output strategy, not an upstream cache fix. The normal `pnpm --dir web build` remains an internal frontend gate. Run down/setup/up after source/dependency changes.

Browser URL and Temporal UI URL are printed on success. Host logs: `logs/{api,worker,web,ollama}.log`. Inspect the exact Compose project via `docker ps -a --filter label=com.docker.compose.project=PROJECT`; then `docker logs EXACT_CONTAINER`. No secrets need to be passed on CLI argv. Private API accepts only the configured Host plus `X-Webchat-Secret`; Next is the browser's only API origin. Never put server secrets in `NEXT_PUBLIC_*` variables.

## Restart, deadlines and failures

`down`/`up` is a restart, not reset. Accepted commands/messages, pending approvals and workflow history remain. Browser reload is not cancellation. Downtime counts toward Temporal activity/child deadlines and approval expiry (24h default); expired work may settle failed/timed-out after restart. Model inference can retry, not resume exact tokens. Replacement must still cancel/join prior work; lifecycle scripts do not bypass cleanup acknowledgments.

If a service exits after readiness (for example database loss makes the API dispatcher exit), inspect logs then rerun up: healthy owned services are reused and the dead service is restarted. Do not infer success from a browser live draft. If down reports unconfirmed exit, preserve receipts and inspect the exact process; no speculative broad SIGKILL/prune is performed. Malformed attachment recovery metadata and uncertain parser cleanup are explicit unavailable/fail-closed conditions. Preserve unclaimed metadata and unknown helper containers; follow [attachment recovery](web-chat-attachments.md), never delete by filename prefix.

The pinned helper is built from `infra/attachment-parser/Dockerfile` and locked package file. Setup builds an exclusive random per-home/per-build tag, keeps Docker's `--iidfile` metadata in private `parser-image.id`, and resolves only that owned tag to the local immutable content ID published in `build.json`. No shared tag is built/looked up. The owned tag is never reused and remains as a reference; setup/down does not delete helper images. This supports Docker/containerd, where `--iidfile` can contain an unroutable config digest rather than the locally addressable image ID. Up verifies/probes that exact local ID with no pull. Every API and worker launch receives `WEBCHAT_PARSER_IMAGE_ID` from that receipt, overriding any inherited value. Each store verifies and captures the full ID once; every parser create uses it with `--pull=never`, never a mutable tag. Another home's setup/integration build or tag movement cannot redirect the next parse. Missing/malformed/unavailable IDs fail closed with setup guidance; removal during runtime cannot trigger fallback. Neither requests nor models select helper images. Changing its image/runtime/package pins requires parser safety and image requalification; a mutable tag alone is not proof. Image admission remains limited to the documented narrow profile; larger-image qualification remains false. Qwen's separate narrow parent reliability deferral also remains, with strict assertions/failure evidence intact.

## Backup and restore (not automatic reset)

Stop this project's application with down to quiesce writes. Preserve **SQL plus the complete attachment root**, config/secrets and relevant build/version evidence. Back up the stopped Docker PostgreSQL volume with an operator-selected volume backup procedure, or start only the verified owned PostgreSQL container and take consistent `pg_dump`/`pg_dumpall` exports for all three databases/roles, then stop it again. Do not copy live PostgreSQL data files. An attachment-only or SQL-only backup is incomplete.

Restore into a separate, explicitly owned project using matching Postgres/server/schema versions and consistent SQL+attachment snapshots. Coordinate credentials, original storage keys and workflow history; do not copy live process ownership receipts. Never restore over shared data or assume an application-only DB restore recovers Temporal execution. Old terminal workflows/transcripts have no migration promise.

There is deliberately **no public destructive reset command**. Removing a Docker volume, attachment root, `.data`, or model cache is destructive and outside setup/up/down. Archive/back up before any independently approved operator cleanup. No `docker system prune`, `compose down -v`, or `rm .data` appears in the lifecycle.

## Internal entrypoints

With the exact private environment from config plus `WEBCHAT_PARSER_IMAGE_ID` from `build.json` for both executables (do not expose secrets; see [manual helper setup](web-chat-attachments.md)), `web-api -migrate` performs SQL migrations; `cmd/web-api` runs the HTTP/dispatcher service; `cmd/web-worker` registers `WebChatSessionWorkflow` and `DocumentAnalysisWorkflow` on `web-chat-agents`, `web-chat-llm`, `web-chat-documents`. One model generation per runner and four document activities remain enforced. The private frontend copy runs `next start --hostname 127.0.0.1 --port PORT`. Compose controls only database/Temporal/UI/init jobs; the parser service profile describes the bounded helper image, not a long-running upload server. Manual internal commands have no automatic ownership receipts: do not mix them with lifecycle-managed ports/resources.
