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
  destinations. IPv6 admission is conservative: global-unicast-shaped `2000::/3`
  minus the documented special-purpose/retired ranges below. This is not proof of
  current allocation or routability. No alternative local-address policy exists.
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
  parser's excessive-nesting errors fail closed. Buffered parsing uses a
  context-checking reader supplying at most 4,096 bytes per read. DOM traversal
  checks context at every node/attribute and copies large text nodes in chunks;
  style inspection and text normalization check at most every 4,096 input bytes
  (plus one UTF-8 code point). Normalization retains only enough output to detect
  the readable-text cap. All phases stay on the calling goroutine; cancellation
  never returns around a detached parser. Cancellation is cooperative, not hard
  preemption inside the pinned parser's individual token operations. Those remain
  bounded by the 2 MiB decoded input and the parser's nesting limit.

### Static address policy sources and limits

The policy is based on the categories in the IANA
[IPv4 special-purpose registry](https://www.iana.org/assignments/iana-ipv4-special-registry/),
[IPv6 special-purpose registry](https://www.iana.org/assignments/iana-ipv6-special-registry/)
and [IPv6 address-space registry](https://www.iana.org/assignments/ipv6-address-space/),
with explicit retired-range treatment from
[RFC 3701](https://www.rfc-editor.org/rfc/rfc3701) (`3ffe::/16`, former 6bone).
The documentation prefixes include `2001:db8::/32` (RFC 3849) and `3fff::/20`
(RFC 9637); tunneling `2002::/16` (6to4) is rejected as a whole.

IPv4 exclusions cover this-network, private/shared, loopback, link-local, protocol
assignments `192.0.0.0/24`, documentation, retired relay `192.88.99.0/24`, benchmark,
multicast and reserved space. IPv6 outside `2000::/3` is rejected, which includes
unspecified/loopback/compatible/mapped, translation (`64:ff9b::/96` and
`64:ff9b:1::/48`), discard/dummy, ULA/site-local/link-local, multicast and SRv6
`5f00::/16`. Within `2000::/3`, the whole `2001::/23` protocol-assignment block,
6to4, documentation and retired 6bone are excluded. This deliberately rejects
some globally reachable special-purpose exceptions too. Globally reachable
public service ranges such as AS112 are not automatically forbidden merely
because they have a special-purpose registry entry.

This is a maintained static exclusion policy, not an online IANA/RIR allocation
lookup, BGP validation or a guarantee that an allowed address is assigned today.
`IsGlobalUnicast` is only an additional shape check, never an allocation
certificate. Registry/RFC references document the policy basis; no live registry
refresh is implied by the tests. New reserved/special-use assignments require
policy review and regression tests. Every answer in a mixed DNS set still has to
pass; neither mapped nor retired addresses are unmapped or retried as public.

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
