# Approved public URL fetch (Task 7)

The actual web worker's `ChatStep` / `ExecuteWebTool` path now installs
`web_fetch` by default. `file_upload` remains unavailable pending the document
work. There is no search, shell, repository or legacy-tool fallback. The exact
`chat.WebToolset` identity and both tool versions are unchanged.

## Consent and results

[`BeginWebTool`](web-chat-approvals.md) remains the authorization authority: it
checks the stored immutable scope and turn seal, registers the physical attempt,
and supplies stored arguments. Denied work never reaches the adapter, DNS or
HTTP. The fetch adapter additionally canonicalizes arguments and verifies the URL
content digest and complete conversation/turn/call/action hash before lookup.
Revocation does not retroactively revoke an already authorized remote read.

The adapter returns `chat.WebToolResult` with readable `text`, `requestedUrl`,
`finalUrl`, `contentType`, and explicit boolean `truncated`. These fields are
stored together in the existing immutable JSON result, under existing attempt
ownership fences. URLs are each at most 2,048 bytes; content type at most 128;
readable text at most 16,000 UTF-8 bytes. No migration is needed (schema 5).
A retry after successful persistence adopts the result without another request.
This is durable result idempotence, **not** exactly-once remote HTTP: if a worker
dies before persisting its response, a recorded authorized retry may fetch again.

The subsequent model step receives the source metadata as JSON tool-result
content. The existing combined 4,000-byte readable-text excerpt allowance still
applies. Excerpt trimming also sets `truncated`, retaining source attribution even
for an empty excerpt. Tool output remains untrusted data under the existing system
policy; page text cannot authorize actions or change tool policy. Full bounded
results remain in SQL; workflow history carries references rather than bodies.

## Network and extraction policy

- HTTP(S) GET only; no URL credentials, caller headers, cookies, Referer,
  authorization forwarding, environment proxy inheritance or automatic link reads.
- Every initial destination and redirect is validated. All DNS answers must be
  public. Reject special-use IPv4, private, loopback, link-local, multicast,
  unspecified, IPv4-mapped IPv6, translation/tunneling/documentation/reserved
  destinations. IPv6 admission is conservative: current global unicast `2000::/3`
  minus special-purpose ranges. No alternative local-address policy exists.
- Dial only the validated literal IPs, never the original hostname a second time.
  Each hop uses a new connection and a new transport, with no pool reuse. HTTP Host
  and HTTPS SNI/certificate verification still use the URL hostname.
- DNS, dialing, TLS handshake, redirects and response body share one 20-second
  operation context (a caller's earlier deadline wins). Dialing and TLS handshake
  are synchronous so their physical work joins before the activity acknowledges
  cleanup. Connections and bodies close on every exit. TLS teardown closes the
  raw socket rather than adding a graceful-notification write beyond cancellation.
  The existing strict accepted-stop/replacement ledger barrier is unchanged. Local
  connection closure is not a claim of revoking a remote server's already-read data.
- At most three redirects. Redirects to credentials, unsupported schemes or
  forbidden addresses fail before dialing them. No response cookies/headers are
  forwarded. Response headers are capped at 64 KiB.
- At most 2 MiB of **decoded** body input, independent of Content-Length. Gzip and
  zlib-wrapped deflate are decoded explicitly; unknown/stacked encodings fail.
  Declared supported character encodings are converted before the decoded bound;
  otherwise text must be UTF-8. Read, compression/checksum and cancellation errors
  discard partial output. Reaching the bounded prefix is explicitly truncated;
  unread tails are not claimed verified.
- Supported media: `text/html`, `text/plain`, `text/markdown`, `application/json`.
  Other media and non-2xx terminal responses fail. HTML uses pinned `x/net/html`
  parsing, removes active/nonreadable subtrees and explicit hidden content, and
  emits text only. It does not run JavaScript, load CSS or resolve links. It is a
  conservative text extractor, not a browser layout/CSS visibility engine. The
  parser's excessive-nesting errors fail closed.

## APIs and test seams

- `tools.FetchArgs{URL string}` and `tools.FetchResult{RequestedURL, FinalURL,
  ContentType, Text string; Truncated bool}`.
- `tools.NewFetcher().Fetch(ctx,args)` is the production network primitive. It does
  not itself grant consent; use it through the authorized activity adapter.
- `tools.NewFetcherWithNetwork(lookup,dial)` injects DNS and a literal-address
  dialer. The same validation and real HTTP transport always run. No environment,
  flag, request argument or runtime setting can enable a localhost bypass or
  replacement transport. Injected implementations must honor context and normal
  `net.Conn` close semantics; tests that deliberately delay cleanup must release
  and join their owned work.
- `activities.NewWebFetchAdapter(fetcher)` creates a `tools.WebAdapter`. Explicit
  `ChatActivities.WebTools` registries can install test dependencies; production
  nil registries install the safe default fetch adapter. Future attachment tasks
  should preserve this default when extending registry construction.

`scripts/test-web-integration.sh fetch` runs isolated real PostgreSQL, migrations,
API/dispatcher and Temporal tests. The new integration uses real HTTP transport
and parser over injected `net.Pipe` wire responses; no public network, localhost
policy exception or live inference. It proves four concurrent independent fetches,
deterministic preflight approvals, stored source/result context, reuse after lost
SQL acknowledgments, denied zero-I/O, and replacement joining physical fetch work.
The mode also retains approval/restart/rollover and older-attempt cleanup guards.
Injected-fault SDK log assertions remain Task 14, not silently asserted here.
