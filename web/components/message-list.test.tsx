// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MessageList, WorkStatus } from "./message-list";
import { initialState, reduceEvent } from "../lib/event-reducer";
import { render, within, cleanup } from "@testing-library/react";
import { afterEach } from "vitest";
afterEach(cleanup);

it.each(["tool.state", "snapshot"])(
  "shows a failed operation on its completed turn from %s, never another turn",
  (type) => {
    let s = initialState("a");
    s.messages = [
      {
        entryId: "question",
        conversationId: "a",
        turnId: "t",
        role: "user",
        text: "Read the page",
        sequence: 1,
      },
      {
        entryId: "answer",
        conversationId: "a",
        turnId: "t",
        role: "assistant",
        text: "A general answer",
        sequence: 2,
      },
      {
        entryId: "other",
        conversationId: "a",
        turnId: "other",
        role: "assistant",
        text: "Unrelated answer",
        sequence: 3,
      },
    ] as any;
    s.turns.t = { id: "t", status: "completed", reason: "" };
    const tool = {
      id: "private-tool",
      turnId: "t",
      name: "web_fetch",
      status: "failed",
      result: { error: "private-network-error" },
      sources: [],
    };
    s = reduceEvent(type === "snapshot" ? initialState("a") : s, {
      id: "1",
      conversationId: "a",
      turnId: null,
      stepId: null,
      attempt: null,
      type,
      data:
        type === "snapshot"
          ? {
              reset: true,
              messages: s.messages,
              turns: Object.values(s.turns),
              tools: [tool],
              commands: [],
              attempts: [],
              before: "",
              metadata: null,
            }
          : tool,
    } as any);
    const { container } = render(<MessageList state={s} />);
    const answer = container.querySelector(
      '[data-message-id="answer"]',
    )! as HTMLElement;
    expect(within(answer).getByRole("status")).toHaveTextContent(
      "A web page could not be read.",
    );
    expect(
      container.querySelector('[data-message-id="question"]'),
    ).not.toHaveTextContent("could not");
    expect(
      container.querySelector('[data-message-id="other"]'),
    ).not.toHaveTextContent("could not");
    expect(container.textContent).not.toMatch(
      /private-tool|private-network-error/,
    );
    expect(container.querySelector("details,pre")).toBeNull();
  },
);

it("reports an attachment failure even before an assistant answer, but not denied/cancelled/successful tools", () => {
  const s = initialState("a");
  s.messages = [
    {
      entryId: "question",
      conversationId: "a",
      turnId: "t",
      role: "user",
      text: "Read file",
    },
  ] as any;
  s.tools.failed = {
    id: "failed",
    turnId: "t",
    name: "file_upload",
    status: "failed",
    result: null,
    sources: [],
  };
  const { container, rerender } = render(<MessageList state={s} />);
  expect(container).toHaveTextContent("An attachment could not be read.");
  for (const status of ["denied", "cancelled", "completed"]) {
    s.tools.failed.status = status;
    rerender(<MessageList state={s} />);
    expect(container).not.toHaveTextContent("could not be read");
  }
});
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
  s.drafts.draft = {
    text: "Still writing",
    attempt: 42,
    bounded: true,
  } as any;
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
