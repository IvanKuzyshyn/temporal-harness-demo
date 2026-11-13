# Durable local web chat

A single-user, loopback-only chat application: **Next.js → Go API → Temporal workers → Ollama**, with conversation metadata/history in Postgres and original attachments in a persistent local directory. No login, hosted model, shell tool, repository editing, or public deployment mode.

## Start here

Install [mise](https://mise.jdx.dev/getting-started.html) and [Docker with Compose](https://docs.docker.com/get-started/get-docker/), and start the Docker daemon. On first use:

```sh
mise trust
mise run setup
mise run up
# When finished:
mise run down
```

`setup` installs pinned Go/Node/pnpm/Python/Ollama/Temporal CLI dependencies, installs the frozen frontend lockfile, builds the isolated attachment parser and fresh production applications, and ensures the enabled exact **qwen3.5:9b** model is downloaded (multi-GB if missing). It leaves no newly owned service running. Run `down` before rebuilding an already-running application. Existing configuration is never overwritten.

`up` backgrounds and checks the complete stack, explicit SQL migrations, all worker queues, helper availability, exact model/runtime identity, and served frontend assets. Defaults: browser **http://127.0.0.1:13000**, Temporal UI **http://127.0.0.1:18080**. Use the printed browser origin exactly. Logs are in `.webchat/logs/`; runtime configuration/credentials and ownership receipts are private files under `.webchat/`.

`down` stops only verified owned processes/containers. It **does not cancel turns, remove volumes, delete attachments, or delete models**. Pending approvals/accepted work survive downtime; Temporal deadlines continue elapsing and may expire before restart. Repeat either command safely. A failed startup rolls back only resources newly started by that operation. Existing shared Ollama is reused only if compatible, and never stopped or reconfigured to resolve a conflict.

### Important qualification limits

The narrow image profile currently requires Ollama **0.40.15**, the recorded exact model digests, and the **MLX/safetensors** runner used in qualification. A stock different runner, even with the same model tag, is **not qualified**: setup/up fail with requalification guidance instead of quietly replacing models. This means the complete currently-qualified image stack is machine/runtime-specific; pinned tools alone do not promise equivalent results on every OS. See [image profile](docs/web-chat-image-profile.md).

Only **qwen3.5:9b** is currently enabled, fixed per conversation. The extensible catalog retains model-driven routing; disabled Gemma requests are rejected, not substituted. Installed Gemma files and historical records are not deleted. The known larger-image Qwen failures and distinct narrow-profile Qwen parent contradictory refusal/stray `</think>` remain **explicitly deferred, not fixed**. Lifecycle success is not semantic certification. No fallback or weakened context, safety, approval, or resource bounds is authorized. [Qualification and retained evidence](docs/web-chat-qualification.md).

## Use the app

Create a conversation, send follow-ups, reopen saved history, or replace/stop the active turn. Attach TXT/Markdown/PDF/DOCX/PNG/JPEG/SVG (one per message, up to 1,048,576 bytes). Upload is not consent: **Approve once**, **Approve all** for this versioned toolset/conversation, or **Deny**. Approval can be revoked. Only `web_fetch` and `file_upload` are available; document analysis uses bounded child workflows and parallel chunks. Markdown does not execute raw HTML. No OCR/search/shell service is exposed.

## Operations and development

- [Lifecycle, ownership, logs, backup and troubleshooting](docs/web-chat-operations.md)
- [Architecture and authority](docs/architecture.md)
- [Testing and CI](docs/testing.md)
- [API contract](docs/web-api.md), [storage](docs/web-chat-storage.md), [attachments](docs/web-chat-attachments.md)
- [Temporal execution](docs/web-chat-temporal.md), [approvals](docs/web-chat-approvals.md), [context](docs/web-chat-context.md)

Public commands are `mise run setup|up|down`. `cmd/web-api`, `cmd/web-worker`, Compose and frontend scripts are internal entrypoints; see operations before invoking them directly.

The previous terminal/coding product and bundled sample project are retired. Historical plans, qualification records and captured terminal replay histories are retained as historical evidence only; there is **no legacy workflow/transcript migration or replay compatibility promise**. Existing `.data/` and other user artifacts are untouched.
