import { it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MessageList, WorkStatus } from "./message-list";
import { initialState } from "../lib/event-reducer";
it("keeps queued/failed messages and action-required notices without diagnostic panels", () => {
  const s = initialState("a");
  s.turns.t = { id: "t", status: "failed", reason: "model_error" };
  s.metadata = {
    pendingApproval: { toolName: "web_fetch" },
    policy: { approvalAll: true },
    dispatcherConnected: false,
  } as any;
  s.messages = [
    {
      entryId: "m",
      conversationId: "a",
      turnId: "t",
      role: "assistant",
      text: "Partial answer",
      completionState: "incomplete",
      context: {
        coverage: {
          recentExchanges: 6,
          omittedExchanges: 2,
          outputTruncated: true,
        },
        accounting: { inputTokens: 123 },
      },
    },
  ] as any;
  s.tools.tool = {
    id: "trace-secret",
    name: "web_fetch",
    arguments: { url: "https://example.com/private-diagnostic" },
    status: "failed",
  } as any;
  s.drafts.draft = { text: "Still writing", attempt: 42, bounded: true } as any;
  const html = renderToStaticMarkup(
    <>
      <WorkStatus state={s} />
      <MessageList state={s} />
    </>,
  );
  expect(html).toContain("Failed");
  expect(html).toContain("Approval needed");
  expect(html).toContain("Partial answer");
  expect(html).toContain("Incomplete");
  expect(html).toContain("Still writing");
  for (const diagnostic of [
    "<details",
    "Context provenance",
    "Bounded, lossy",
    "tool sources omitted",
    "inputTokens",
    "trace-secret",
    "private-diagnostic",
    "attempt 42",
    "Saved state",
    "Work status",
  ])
    expect(html).not.toContain(diagnostic);
});
