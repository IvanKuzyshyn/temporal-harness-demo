import { it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MessageList, WorkStatus } from "./message-list";
import { initialState } from "../lib/event-reducer";
it("exposes failed pending approval policy and lossy coverage without opening details", () => {
  const s = initialState("a");
  s.loaded = true;
  s.turns.t = { id: "t", status: "failed", reason: "model_error" };
  s.metadata = {
    pendingApproval: {
      toolName: "web_fetch",
      arguments: { url: "https://example.com" },
      deadline: "tomorrow",
    },
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
      sequence: 1,
      context: {
        coverage: {
          sourceLimited: true,
          omittedExchanges: 0,
          summaryVersion: 1,
          recentExchanges: 6,
          summaryBoundary: 2,
          evidence: { included: [], eligible: 2, omitted: 2, deduplicated: 0 },
          quotations: {
            included: [],
            eligible: 3,
            omitted: 3,
            deduplicated: 0,
          },
        },
        accounting: {
          method: "bound",
          inputTokens: 123,
          outputTokens: 2000,
          imageTokens: 0,
          reserve: 2384,
          total: 4507,
        },
      },
    },
  ] as any;
  const html = renderToStaticMarkup(
    <>
      <WorkStatus state={s} />
      <MessageList state={s} />
    </>,
  );
  expect(html).toContain("model_error");
  expect(html).toContain("Approval needed");
  expect(html).toContain("Conversation approval enabled");
  expect(html).toContain("Bounded, lossy context");
  expect(html).toContain("2 tool sources omitted");
  expect(html).toContain("3 quotation pairs omitted");
  expect(html).toContain("not measured usage");
  expect(html).toContain("Incomplete");
});
