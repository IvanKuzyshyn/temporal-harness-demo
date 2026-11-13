# Testing and CI

[Quickstart](../README.md) · [Operations/ownership](web-chat-operations.md) · [Architecture](architecture.md)

## Required local gates

```sh
mise trust
mise install
mise exec -- go build ./...
mise exec -- go vet ./...
mise exec -- go test -p 1 -count=1 ./...
mise exec -- go test -race -p 1 ./...
mise exec -- bash scripts/dev/tests/lifecycle_test.sh
mise exec -- pnpm --dir web install --frozen-lockfile
mise exec -- pnpm --dir web test
mise exec -- pnpm --dir web typecheck
mise exec -- pnpm --dir web build
```

Go packages run serialized because retained transport/timing tests have exhibited intermittent failures. Do not hide a failing run by reporting only a later green run. `TestRuntimeCancellationPreservesShutdownRPCs` still retains its three-second remote ConnEnd assertion; Task13 reproduced it against both old and web runners after local cleanup. Diagnostic stacks are emitted before server teardown. A green run is not a fix or provider/GPU reclamation proof.

Lifecycle tests exercise real locking, real supervisor/child shutdown (including Node retitling and supervisor death), stale/reused PID protection, and orchestrator rollback/recovery with narrow external-driver fault doubles. Cases include repeated setup/up/down, Docker/model/port preflight failure, migration/start/readiness failure, shared Ollama reuse, temporary Ollama cleanup, retained failed-rollback journal, controller death, SSE shutdown ordering and isolated fresh frontend source/output. These tests alone **do not certify** real Docker/browser/model persistence.

## Real lifecycle acceptance

Use documented public commands with a fresh absolute `WEBCHAT_HOME`, unused ports and an exact owned project. Shared Ollama may be reused only if compatible and must remain untouched. Preserve receipts/logs before and after:

1. Setup twice; confirm no newly owned running services, frozen dependencies, exact models/helper, unchanged checkout/user configuration and new independent build outputs.
2. Up twice; compare process/container identities (no duplication), real readiness, browser CSS against fresh assets and all queue pollers.
3. In the browser create a conversation, obtain a saved reply, upload a known original, submit it and leave approval pending.
4. Down with the browser/SSE open; verify exact owned process/container exit, closed owned ports and preserved SQL volume/originals. No workflow cancel Update may be sent.
5. Up/reload the same conversation: same durable message IDs/content, original checksum/bytes, pending approval/action/deadline. Decide the approval; verify real tool/child completion and saved result.
6. Exercise safe startup failure/rollback, stale receipt and concurrency tests. Audit shared endpoints unchanged and stop only exact owned resources. Keep all persistent test artifacts unless separately authorized to destroy them.

Lifecycle success is not semantic model qualification. Preserve both the larger-image Qwen failures and distinct narrow-profile parent contradictory refusal/stray `</think>` exception. No fallback, blanket Qwen ignore, or relaxed final-answer/security/context assertion is allowed. See [qualification](web-chat-qualification.md), [image profile](web-chat-image-profile.md) and [retained diagnostic](web-chat-r2-diagnostic.md).

## Integration and browser suites

`scripts/test-web-integration.sh storage|api|chat|replacement|approval|fetch|attachments|analysis|context|stream` creates uniquely named PostgreSQL and private Temporal test resources; it never targets the developer stack. It normally removes only its verified test-owned resources, including test volume, at the end. Set `WEBCHAT_TEST_RETAIN=1` when instructed to retain all test data: cleanup then stops owned services but keeps containers/SQL volume, Temporal DB, receipts and logs. Task13 used this mode. Direct `-tags integration` tests require explicit isolated `WEBCHAT_TEST_DATABASE_URL` (`webchat_test_` database) and Temporal endpoint; never point them at a working conversation database.

`chat` covers the full web integration set with mock/injected models and real SQL/Temporal. For all parser stages plus the full set, use `WEBCHAT_TEST_CHAT_RUN=. scripts/test-web-integration.sh context`. `context` includes attachment/parser, context, child/replacement and hard-death boundaries. Tests retain meaningful fault assertions. Injected invalid/cancellation/shutdown SDK logs are expected but some log assertions remain assigned Task14; output is not described as pristine. Live image/parent tests are separate opt-in qualification gates, not automatically certified by mock integration.

Frontend Vitest covers interaction/security/stream reconciliation. Playwright `web/tests/` uses explicit `WEBCHAT_TEST_BROWSER_URL` and owns browser contexts; the live app runner is opt-in and separately provisioned. Its exact assertions remain unchanged, including known Qwen semantic failures; do not blanket-ignore them. Browser source is only presentation, never a substitute for SQL/workflow evidence.

CI runs pinned tools, Go build/vet/race(-p1), lifecycle tests, frozen frontend install/test/typecheck/build, and the **full** real isolated web integration set. Task13's broad gate exposed four inherited failures, reproduced on base `a21098e`: `TestAdmissionOutageRollsOverBeforeRestoration`, `TestDatabaseOutageDuringFinalPersistence`, `TestOverlappingChatModelAttemptsRealTemporal`, and `TestPersistenceExhaustionRecoversAdmittedInboxAndDispatcher`. Older event-count assertions encountered current `message.delta` rows. The four tests now validate every event's domain/scope: only legitimate bounded registered-attempt previews are provisional, and there must still be exactly one correct canonical completion/final assistant per settled turn (zero before persistence). Unknown/malformed events and premature/duplicate/missing completion remain failures, with 21 negative controls in the default Go suite; all pages are checked against SQL totals. No production behavior changed for this compatibility correction. The scoped four-test and subsequent **full** integration gates passed; original base/current RED logs remain in the Task13 report evidence. These failures were separate from the two Qwen qualification exceptions. CI does not download/qualify the local multi-GB MLX models; actual public lifecycle/browser acceptance is a separately recorded host gate.

## Historical evidence

Old terminal plans, screenshots references, product qualification and `docs/historical/terminal-replay/` histories describe the retired product. The old capture tool depended on deleted sample/coding workflows and was removed rather than advertised as a web replay generator. Captures are retained byte-identically for provenance only. New web restart/unit/integration evidence is not a claim of replay compatibility with them. No existing `.data` or old user artifacts are deleted.
