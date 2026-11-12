# Private web-chat API (Tasks 3–5)

This is the **Next.js server-to-Go contract**, without an `/api` prefix. The
browser talks only to Next.js; Next.js adds `/api` to its public route names.
Postgres owns content and intent. Temporal owns execution/admission in Task 4.
See [the text-chat runtime](web-chat-temporal.md) for execution, durable receipt
handshake, failure bounds and verification.
**202 means durable intent, never an assistant answer, cancellation completion,
or permission to access an attachment.**

## Run and readiness

Build with the existing pins: `mise exec -- go build -o bin/web-api ./cmd/web-api`.
Export configuration explicitly (the binary does not automatically source `.env`):

- `WEBCHAT_API_LISTEN=127.0.0.1:18081` (a literal loopback IP and nonzero port;
  IPv6 `[::1]:18081` also works; wildcard/DNS listen addresses are rejected).
- `WEBCHAT_BROWSER_ORIGIN=http://localhost:3000`, exact loopback HTTP(S) origin,
  no path/trailing slash, userinfo, query or fragment.
- `WEBCHAT_PROXY_SECRET`: independent 32 random bytes in lowercase hex; generate
  with `openssl rand -hex 32`. Never use a `NEXT_PUBLIC_` variable.
- `WEBCHAT_DATABASE_URL`: explicit `postgres://webchat:<password>@127.0.0.1:<port>/<appdb>`.
  Use a separately generated password, at least 24 characters; obvious
  placeholders are rejected. `.env.example` is deliberately **not runnable with
  its placeholder credentials**. Existing database volumes retain their original
  passwords; edit credentials deliberately rather than reinitializing volumes.

`bin/web-api -migrate` applies embedded migrations and exits. `bin/web-api` requires
schema version 4 and serves HTTP. Effective pgx user/database/startup parameters
are validated before connecting, including query/environment/service overrides;
only the validated config is used. Application identity must be `webchat`, never
Temporal's database/role. Runtime connection hosts and fallback hosts must be
literal loopback IPs. Errors never echo the URL/password. Migrations retain the
Task 2 effective-configuration safety checks.

The executable supervises the real Temporal gateway/dispatcher. Set
`WEBCHAT_TEMPORAL_ADDRESS` (default `127.0.0.1:7233`); run `web-worker` separately.
`WEBCHAT_EXECUTION_BACKEND` is rejected if set. The API never runs a model itself.
Atomic health observations populate `Config.GatewayConnected`, `WorkersPolling`
and `ReconciliationReady`; no request blocks on a Temporal health probe.

All routes, including health, require the private header contract below:

- `GET /healthz`: 200 if the process responds; includes DB/dispatcher status.
- `GET /readyz`: 200 only if storage, actual Temporal transport, recent active-queue
  worker pollers, and reconciliation are ready; otherwise 503 with the same JSON.
  `acceptingCommands:true` still means DB-backed intent can be queued, not executed.
  Poller observations can be stale for up to 90s and do not prove model availability.
- DB availability does not prove workers/models are available. `/models` reports
  availability and capabilities as `unknown`/`unverified`, not invented probes.
- HTTP request operations have a 5-second context; connections have 5-second
  header, 10-second read, 15-second write, 30-second idle timeouts and a 16KiB
  header limit. SIGTERM/SIGINT drains HTTP for up to 10 seconds. Shutdown never
  submits stop commands, deletes outbox rows, or changes execution state.

## Next.js security/header contract

Construct upstream requests; do not blindly forward browser headers:

1. Upstream Host **must exactly equal** `WEBCHAT_API_LISTEN`.
2. Set exactly one `X-Webchat-Secret` to the server-side secret. Comparison hashes
   both strings to fixed length and uses constant-time comparison. Never return
   this header to the browser or log it.
3. Authenticate/validate the browser request at the Next.js boundary: exact
   configured browser Host/Origin and same-origin fetch context. The Go upstream
   also rejects a supplied Origin unless it exactly equals
   `WEBCHAT_BROWSER_ORIGIN`, and rejects supplied `Sec-Fetch-Site` except
   `same-origin`, and supplied `Sec-Fetch-Mode` except `cors`/`same-origin`.
4. The server-to-server request may omit Origin/Sec-Fetch headers after Next.js
   checks them, or forward that validated context. Cross-site, same-site, null
   Origin, navigation mode, arbitrary Host, missing/wrong secret fail with 403.
   Do not copy browser Host, cookies, Authorization, or arbitrary forwarding
   headers to the private API. `X-Forwarded-*` headers do not grant trust.
5. No CORS opt-in or preflight success is provided. Responses are `no-store`,
   `application/json`, and `X-Content-Type-Options:nosniff`.

Loopback is not authentication. A network-accessible deployment needs a separate
security design. The proxy secret protects local-browser direct access; it does
not defend against a local process that can read server credentials.

## Routes, identities, paging and status

All JSON mutations require `Content-Type: application/json` (parameters allowed),
a single object, valid UTF-8, no unknown/duplicate keys, null field values, or
trailing data. Maximum encoded body is **400KiB** (allows escaped 64KiB text);
maximum decoded prompt is **65,536 UTF-8 bytes**; at most four distinct attachment
IDs. IDs are nonzero canonical lowercase hyphenated UUIDs. A message must contain
text or attachments. References are ownership-checked in the acceptance transaction.
Uploads alone do not grant content access.

- `GET /models` → 200 `Models`.
- `POST /conversations` (`CreateRequest`) → 201 `Created`, including retries.
  Omitted model defaults to **qwen3.5:9b**; explicit empty/null/unknown tags fail.
- `GET /conversations?cursor=...&limit=50` → 200 `ConversationPage`.
- `GET /conversations/{id}` → 200 `Metadata`.
- `GET /conversations/{id}/messages?before=...&limit=50` → 200 `MessagePage`.
- `POST /conversations/{id}/messages` (`MessageRequest`) → 202 `Receipt`.
- `POST /conversations/{id}/replace` (`ReplaceRequest`) → 202 `Receipt`.
- `POST /conversations/{id}/stop` (`StopRequest`) → 202 `Receipt`.
- `POST /conversations/{id}/approvals/{approvalId}/decision` (`DecisionRequest`)
  → 202 `Receipt`.
- `POST /conversations/{id}/approval-policy/revoke` (`RevokeRequest`) → 202 `Receipt`.
- `GET /conversations/{id}/commands/{commandId}` → 200 `Receipt`. Mutations return
  this relative URL in `Location`. Poll it to resolve asynchronous admission.
- Reserved: `POST /conversations/{id}/attachments` (multipart original file),
  `GET /conversations/{id}/attachments/{attachmentId}` (binary safe download),
  `GET /conversations/{id}/events` (`text/event-stream`, `Last-Event-ID`). These
  return **501 `not_implemented`**, never fake upload IDs, bytes, or event streams.

Retain request IDs across reload/network retries. Creation has its own global,
permanent request-ID domain; same ID/same selected model returns the same
conversation (with current metadata), changed model is 409. Commands deduplicate
per `(conversationId, requestId)` **across command kinds**, indefinitely. Retries
return the original command/turn with its **current** result state, still HTTP 202.
`payloadDigest` is an optional assertion; the server always calculates it. An
assertion mismatch is 409, not acceptance. Creation hashes UTF-8
`{"model":"<selected tag>"}`. Commands hash Go's deterministic JSON encoding of
`{kind,payload}` from `chat.CommandInput.Digest()` (field order/omitempty as in
`internal/chat/contracts.go`, Go JSON string escaping); use the server's returned
digest rather than attempting ad hoc browser JSON canonicalization. Empty/nil
attachment arrays normalize; text, array order, control targets, hash and decision
remain significant. Request IDs are not part of payload digests.

`limit` is 1–200. Duplicate/unknown pagination query keys and malformed cursors
are 400. Cursors are opaque, versioned, not credentials. History pages contain
oldest-to-newest messages; no `before` means the latest page; `before:""` means
no older page. History cursors are conversation-bound and preserve committed
message order, not timestamps. Sidebar starts with most-recent activity and
captures a committed activity watermark; subsequent pages preserve membership
and ordering at that watermark, even if unseen conversations become active.
Metadata within those pages is current. New activity appears on refreshing the
first page. `cursor:""` means no next page. There is no cursor expiry/retention
policy yet; the durable activity index is intentionally retained.

HTTP failures use `Error` below: 400 invalid payload/query/constraint; 403 private
boundary failure; 404 absent conversation/receipt or missing, foreign, not-ready
references (including a mismatched approval hash); 409 reused identity with
changed content/digest; 413 body/prompt too large; 415 wrong media type; 503 DB or
infrastructure unavailable; 501 reserved transport routes. Root known-route
method mismatches return 405; other unknown route/method combinations return 404.
No SQL, connection string, stack trace, upstream error text, or credentials enter
these errors. `/readyz` is the exception: its 503 body is `Health`, not `Error`.

Stale active targets and approval decisions are **not** authorized/adjudicated by
Postgres. They can receive 202 and later `resultState:"rejected"` with e.g.
`result:{"state":"rejected","code":"stale_target"}`. Display rejection rather
than an endless spinner; never treat queued user text as an assistant answer.
Accepted admission still does not mean the turn is complete. During gateway
outages commands remain durably pending and readiness says why. Task5 supports
stop and text replacement. The workflow claims the exact expected active target;
competing distinct controls return `control_conflict`, stale/settling targets
return `stale_target`. Accepted control results include `targetTurnId` and, for
replacement, `replacementTurnId`. Poll the receipt and conversation/history
projections separately: accepted does not mean cleanup is complete. Replacement
starts only after joined cleanup and durable target cancellation, ahead of queued
normal messages. For an accepted control, any missing cleanup acknowledgment across
**all** model invocations of its target (including older timed-out attempts) leaves
`cancelling_interrupted` in workflow state and `cancelling` in SQL; no newer turn
runs. Later retry cancellation/success cannot bypass this durable barrier. Ordinary
completion/failure without an accepted control retains retry-safe hard-worker-loss
recovery: missing cleanup acknowledgments do not block canonical settlement or
normal follow-ups, and are not falsely marked confirmed. See the runtime document for
completion linearization, recovery and operator limits. Approvals/revocation and
new attachment-bearing messages/replacements still reject `invalid_command`.

Metadata is a repeatable-read Postgres projection. `observedAt` is an API read
observation, **not a workflow confirmation**. `authoritative:false` and
`workflowConfirmedAt:null` are deliberate. `activeTurn` and `pendingApproval` may
lag or be null; they never authorize controls/content. Approval policy revision
is available, but Task 2 did not persist the effective approve-all flag, so
`approvalAll:"unknown"` remains honest until Task 4 adds an authoritative policy
projection. Do not infer the flag from the revision. Command lookup reads the
latest committed admission receipt, not live execution status.

## JSON schemas

Draft 2020-12. Select the named `$defs` for each route above. `additionalProperties`
is false for request/response records unless explicitly open. Semantic checks
not expressible here (UTF-8 **byte** limit, nonzero UUID, distinct/owned references,
message content, digest equality, freshness) are enforced/described above.

```json
{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "$defs":{
    "ID":{"type":"string","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$","not":{"const":"00000000-0000-0000-0000-000000000000"}},
    "NullableID":{"anyOf":[{"$ref":"#/$defs/ID"},{"type":"null"}]},
    "Digest":{"type":"string","pattern":"^[0-9a-f]{64}$"},
    "DigestAssertion":{"anyOf":[{"$ref":"#/$defs/Digest"},{"const":""}]},
    "Model":{"enum":["qwen3.5:9b","gemma4:e4b"]},
    "Time":{"type":"string","format":"date-time"},
    "Attachments":{"type":"array","maxItems":4,"uniqueItems":true,"items":{"$ref":"#/$defs/ID"}},
    "Text":{"type":"string","description":"Maximum 65536 UTF-8 bytes"},
    "CreateRequest":{"type":"object","additionalProperties":false,"required":["requestId"],"properties":{"requestId":{"$ref":"#/$defs/ID"},"model":{"$ref":"#/$defs/Model"},"payloadDigest":{"$ref":"#/$defs/DigestAssertion"}}},
    "MessageRequest":{"type":"object","additionalProperties":false,"required":["requestId"],"properties":{"requestId":{"$ref":"#/$defs/ID"},"payloadDigest":{"$ref":"#/$defs/DigestAssertion"},"text":{"$ref":"#/$defs/Text"},"attachmentIds":{"$ref":"#/$defs/Attachments"}}},
    "ReplaceRequest":{"type":"object","additionalProperties":false,"required":["requestId","expectedActiveTurnId"],"properties":{"requestId":{"$ref":"#/$defs/ID"},"payloadDigest":{"$ref":"#/$defs/DigestAssertion"},"expectedActiveTurnId":{"$ref":"#/$defs/ID"},"text":{"$ref":"#/$defs/Text"},"attachmentIds":{"$ref":"#/$defs/Attachments"}}},
    "StopRequest":{"type":"object","additionalProperties":false,"required":["requestId","expectedActiveTurnId"],"properties":{"requestId":{"$ref":"#/$defs/ID"},"payloadDigest":{"$ref":"#/$defs/DigestAssertion"},"expectedActiveTurnId":{"$ref":"#/$defs/ID"}}},
    "DecisionRequest":{"type":"object","additionalProperties":false,"required":["requestId","actionHash","decision"],"properties":{"requestId":{"$ref":"#/$defs/ID"},"payloadDigest":{"$ref":"#/$defs/DigestAssertion"},"actionHash":{"$ref":"#/$defs/Digest"},"decision":{"enum":["once","all","deny"]}}},
    "RevokeRequest":{"type":"object","additionalProperties":false,"required":["requestId"],"properties":{"requestId":{"$ref":"#/$defs/ID"},"payloadDigest":{"$ref":"#/$defs/DigestAssertion"}}},
    "Conversation":{"type":"object","additionalProperties":false,"required":["id","title","model","workflowId","lifecycle","policyRevision","createdAt","updatedAt"],"properties":{"id":{"$ref":"#/$defs/ID"},"title":{"type":"string"},"model":{"$ref":"#/$defs/Model"},"workflowId":{"type":"string"},"lifecycle":{"enum":["open","closed"]},"policyRevision":{"type":"integer","minimum":0},"createdAt":{"$ref":"#/$defs/Time"},"updatedAt":{"$ref":"#/$defs/Time"}}},
    "Created":{"type":"object","additionalProperties":false,"required":["conversation","payloadDigest"],"properties":{"conversation":{"$ref":"#/$defs/Conversation"},"payloadDigest":{"$ref":"#/$defs/Digest"}}},
    "ConversationPage":{"type":"object","additionalProperties":false,"required":["conversations","cursor"],"properties":{"conversations":{"type":"array","maxItems":200,"items":{"$ref":"#/$defs/Conversation"}},"cursor":{"type":"string"}}},
    "Receipt":{"type":"object","additionalProperties":false,"required":["commandId","turnId","payloadDigest","dispatchState","resultState","result"],"properties":{"commandId":{"$ref":"#/$defs/ID"},"turnId":{"$ref":"#/$defs/NullableID"},"payloadDigest":{"$ref":"#/$defs/Digest"},"dispatchState":{"enum":["pending","dispatched"]},"resultState":{"enum":["pending","accepted","rejected"]},"result":{"type":["object","null"],"description":"Immutable gateway projection; new dispatcher writes GatewayResult. Existing storage projections may have other JSON object fields."}}},
    "GatewayResult":{"type":"object","additionalProperties":false,"required":["state"],"properties":{"state":{"enum":["pending","accepted","rejected"]},"code":{"enum":["stale_target","invalid_approval","approval_expired","conversation_closed","policy_conflict","control_conflict","invalid_command"]},"targetTurnId":{"$ref":"#/$defs/ID"},"replacementTurnId":{"$ref":"#/$defs/ID"}},"if":{"properties":{"state":{"const":"rejected"}}},"then":{"required":["code"]}},
    "Message":{"type":"object","additionalProperties":false,"required":["entryId","conversationId","turnId","role","text","attachmentIds","logicalStep","resultKind","completionState","sequence","createdAt"],"properties":{"entryId":{"$ref":"#/$defs/ID"},"conversationId":{"$ref":"#/$defs/ID"},"turnId":{"$ref":"#/$defs/ID"},"role":{"enum":["user","assistant"]},"text":{"type":"string"},"attachmentIds":{"$ref":"#/$defs/Attachments"},"logicalStep":{"type":"integer","minimum":0},"resultKind":{"type":"string","minLength":1},"completionState":{"enum":["completed","incomplete"]},"sequence":{"type":"integer","minimum":1},"createdAt":{"$ref":"#/$defs/Time"}}},
    "MessagePage":{"type":"object","additionalProperties":false,"required":["messages","before"],"properties":{"messages":{"type":"array","maxItems":200,"items":{"$ref":"#/$defs/Message"}},"before":{"type":"string"}}},
    "ActiveTurn":{"type":"object","additionalProperties":false,"required":["id","status","updatedAt"],"properties":{"id":{"$ref":"#/$defs/ID"},"status":{"enum":["running","awaiting_approval","cancelling"]},"updatedAt":{"$ref":"#/$defs/Time"}}},
    "PendingApproval":{"type":"object","additionalProperties":false,"required":["id","toolCallId","actionHash","deadline","policyRevision"],"properties":{"id":{"$ref":"#/$defs/ID"},"toolCallId":{"$ref":"#/$defs/ID"},"actionHash":{"$ref":"#/$defs/Digest"},"deadline":{"$ref":"#/$defs/Time"},"policyRevision":{"type":"integer","minimum":0}}},
    "Freshness":{"type":"object","additionalProperties":false,"required":["source","authoritative","observedAt","workflowConfirmedAt"],"properties":{"source":{"const":"postgres_projection"},"authoritative":{"const":false},"observedAt":{"$ref":"#/$defs/Time"},"workflowConfirmedAt":{"type":"null"}}},
    "Metadata":{"type":"object","additionalProperties":false,"required":["conversation","activeTurn","pendingApproval","policy","freshness","dispatcherConnected"],"properties":{"conversation":{"$ref":"#/$defs/Conversation"},"activeTurn":{"anyOf":[{"$ref":"#/$defs/ActiveTurn"},{"type":"null"}]},"pendingApproval":{"anyOf":[{"$ref":"#/$defs/PendingApproval"},{"type":"null"}]},"policy":{"type":"object","additionalProperties":false,"required":["revision","approvalAll"],"properties":{"revision":{"type":"integer","minimum":0},"approvalAll":{"const":"unknown"}}},"freshness":{"$ref":"#/$defs/Freshness"},"dispatcherConnected":{"type":"boolean"}}},
    "Models":{"type":"object","additionalProperties":false,"required":["models"],"properties":{"models":{"type":"array","minItems":2,"maxItems":2,"items":{"type":"object","additionalProperties":false,"required":["id","default","availability","capabilities"],"properties":{"id":{"$ref":"#/$defs/Model"},"default":{"type":"boolean"},"availability":{"const":"unknown"},"capabilities":{"type":"object","additionalProperties":false,"required":["text","tools","images","imageAdmissionQualified"],"properties":{"text":{"const":"unverified"},"tools":{"const":"unverified"},"images":{"const":"unverified"},"imageAdmissionQualified":{"const":false}}}}}}}},
    "Health":{"type":"object","additionalProperties":false,"required":["alive","storageReady","acceptingCommands","dispatcher","workersPolling","reconciliationReady"],"properties":{"alive":{"const":true},"storageReady":{"type":"boolean"},"acceptingCommands":{"type":"boolean"},"dispatcher":{"enum":["unavailable","connected"]},"workersPolling":{"type":"boolean"},"reconciliationReady":{"type":"boolean"}}},
    "Error":{"type":"object","additionalProperties":false,"required":["error"],"properties":{"error":{"type":"object","additionalProperties":false,"required":["code"],"properties":{"code":{"enum":["forbidden","invalid_request","invalid_json","invalid_query","not_found","reference_not_found","conflict","body_too_large","prompt_too_large","unsupported_media_type","storage_unavailable","not_implemented","method_not_allowed"]}}}}},
    "Event":{"type":"object","additionalProperties":false,"required":["id","conversationId","turnId","logicalStep","attempt","sequence","type","payload"],"properties":{"id":{"$ref":"#/$defs/ID"},"conversationId":{"$ref":"#/$defs/ID"},"turnId":{"$ref":"#/$defs/NullableID"},"logicalStep":{"type":["integer","null"],"minimum":0},"attempt":{"type":["integer","null"],"minimum":1},"sequence":{"type":"integer","minimum":1},"type":{"type":"string","minLength":1},"payload":{"type":"object","description":"Type-specific bounded lifecycle data, not raw attachment/image bytes. Event names and payload variants are owned by the later workflow/transport tasks."}}}
  }
}
```

`Event` is the **reserved envelope**, not an implemented streaming promise. The
future SSE `id` is the decimal per-conversation `sequence`; `data` is this JSON.
Replay is exclusive after `Last-Event-ID`. Logical step/attempt must either both
be null or both present, and identify a turn when present. Stored event payloads
are capped at 64KiB; future transport must batch deltas, bound slow-client buffers,
replace drafts by attempt identity, and define type-specific payloads and
expired-cursor snapshot signaling before enabling SSE. No event can certify
assistant completion before the final message/status transaction is durable.
No upload/download success schema exists yet because those routes never succeed.

## Dispatcher / Task 4 integration contract

Exported `dispatch.SessionGateway.Submit(context.Context, dispatch.Command)
(dispatch.CommandResult, error)` receives **all five** command kinds. `Command`
contains CommandID, original RequestID, ConversationID, TurnID (empty for controls
without a new turn), WorkflowID, Model, PayloadDigest, Kind, typed Payload, and
per-conversation Sequence. Submit must honor the context and must use persisted
**CommandID as Temporal Update ID**, never a fresh UUID or RequestID. Preserve
original RequestID/digest/turn through all calls. Workflow-side deduplication must
survive Continue-As-New and cover the durable outbox's unbounded retry horizon.
A gateway error always means an ambiguous/retryable outcome to this layer;
return a valid `CommandResult{State:"rejected",Code:...}` for a durable domain
rejection. Unknown/malformed gateway results are retried, not trusted. Valid
states are pending, accepted, rejected; rejection requires a public code from the
schema. Do not return model text, secrets, or raw SDK errors as result data. Task5 adds
bounded `targetTurnId`/`replacementTurnId` on accepted control receipts and
`control_conflict` as an asynchronous rejection code; these IDs refer to the
original authoritative target and already-created replacement, not a newer turn.

`dispatch.New(repo, gateway, Options)` requires an injected gateway;
`Run(ctx)` reconciles until cancellation or exhausted repository-failure budget;
`RunOnce(ctx)` processes at most one claim. Defaults: 30-second lease, 10-second
gateway deadline, 5-second storage-call deadline, exponential command retry
1–60 seconds, 250ms idle/repository-retry poll, at most 32 commands per burst.
Timeout bounds leave lease budget for acknowledgment. Gateway errors/pending
results schedule durable retries; there is no intent retry cap, deletion or dead
letter. Cancellation leaves unacknowledged leases to expire. Gateways must honor
context; abandoning arbitrary Go code cannot safely force cancellation.

`Run` tolerates two repository failures without a successful worked cycle,
waiting PollInterval after each, then returns
**`dispatch.ErrRepositoryUnavailable`** on the third. Only a non-error cycle that
worked a command resets this budget (`RunOnce` returns `worked=true, err=nil`);
successfully persisted acknowledgements/retries provide such progress. **Empty
claim polls do not reset it**: failed acknowledgements leave leased commands
ineligible for claiming, so an empty poll alone proves no write recovery. This
conservative budget also retains earlier claim failures while idle until a
command is successfully worked. Claim and acknowledgement errors both count;
ambiguous gateway errors with successfully persisted retries do not. The sentinel
contains only `dispatcher repository unavailable`, deliberately does **not** wrap raw SQL/connection errors, and is safe
to report. Cancellation returns the context error instead. No intents are removed
when the loop stops. `RunOnce` remains a lower-level interface returning repository
errors; callers must not expose/log them verbatim.

**Task 4 supervisor obligation:** observe `Run`'s return with `errors.Is(err,
dispatch.ErrRepositoryUnavailable)`, report that sanitized failure, and either
fail service supervision or mark reconciliation unavailable and restart it with
bounded backoff. Never silently restart in a tight loop or ignore the return.
A transport-connected callback is not DB or reconciliation readiness, and a
successful DB Ping does not prove claim/ack SQL is working. Track the supervisor's
reconciliation state separately and include it in future overall readiness; the
queue-only executable in this task still has no running production dispatcher.

Task4 implements the supervisor obligation above by failing service supervision
on the sanitized sentinel, joining HTTP/dispatcher/health goroutines before
closing clients. It does not silently restart reconciliation.

New Task3 repository methods: `CreateConversationOnce`, `ListConversations`,
`GetConversationProjection`, `GetCommand`, `Ping`, `ClaimCommands`, `SettleClaim`,
and `RuntimePoolConfig`. See their Go signatures for exact types. Claim tokens
are fencing/ownership identities only, never Temporal identities. SQL selects due
rows with `FOR UPDATE SKIP LOCKED`, sets lease_token/lease_until, increments
attempts, and returns persisted identities. Acknowledgment compares the token,
unexpired lease, and pending result in one UPDATE. Stale owners get
`storage.ErrLeaseLost`; a crash after remote acceptance and before DB ack safely
retries the same remote identity. Terminal results are not reclaimed/overwritten.
The retained `RecordCommandResult` locks the command row **before** inspecting
its state, in the existing conversation→command lock order. `SettleClaim` locks
only the command row and never takes the conversation lock, so mixed writers
serialize without a reverse lock-order dependency. The first terminal write wins:
legacy identical terminal retries succeed (JSONB equality), changed terminal or
pending-resurrection attempts conflict; stale/terminal claims return ErrLeaseLost.
There is no cross-database exactly-once claim.

Ordering is deliberate: a normal `message` cannot pass an earlier unresolved
normal message in its conversation. A permanently unavailable head therefore
blocks **that normal-message lane**, preserving FIFO. Controls (`replace`, `stop`,
`decide`, `revoke`) bypass it and each other when earlier commands are retrying;
other conversations continue. Due-time scheduling moves failed commands behind
ready work. The workflow must adjudicate races/stale controls, enforce replacement
priority, and never use outbox Sequence alone as authority. Control calls can
arrive out of order or concurrently from different dispatchers; durable decision
and policy semantics belong to Temporal. Acceptance sequence is diagnostic and
stable, not a mandate to wait for a missing control. Claiming one item per network
call avoids expiring a batch's leases while earlier calls are still running.

Migration **000002** adds command_sequence/sequence, create_request_id,
lease_token, lease_until, attempts, next_attempt_at, and durable sidebar activity
history/counter. Original 000001 is unchanged. Conversation locks allocate
command order transactionally. A single small sidebar counter serializes activity
index publication to commit order; retained activity rows preserve snapshot
paging under concurrent updates. This favors correctness over high-throughput
multi-user scalability (this is a local single-user demo). Do not garbage-collect
that history without an explicit cursor-expiry policy. Projection writes and
outbox delivery never authorize tools or complete assistant responses.

## Verification and limits of evidence

```sh
mise exec -- go test ./internal/api/... ./internal/dispatch/...
scripts/test-web-integration.sh api
scripts/test-web-integration.sh storage
mise exec -- go build -p 1 ./...
mise exec -- go vet -p 1 ./...
mise exec -- go test -p 1 ./...
```

API integration uses real pinned Postgres, real SQL, the real HTTP handlers, and
real API process restart over loopback. Dispatch uses an **injected fake gateway**
with failure before admission, ambiguous failure after admission, and lost DB
acknowledgment. It proves recoverable application intent and stable delivery
identity, **not Temporal/Continue-As-New/worker/model durability**. The runner
retains storage migration cycle, 000001→000002 populated upgrade, actual Postgres
restart, receipt retry and DB isolation checks; race tests are serialized.
Every invocation creates a UUID project/database and Docker-selected loopback
port, uses independent generated test app credentials, and cleans only its own
container/network and ownership-verified volume. Default tests never leave an
API running. No live-model calls or change to the deferred Qwen image gate.
