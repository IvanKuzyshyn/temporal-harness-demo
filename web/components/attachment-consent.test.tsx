// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { ApprovalCard } from "./approval-card";
import { MessageList } from "./message-list";
import { initialState } from "../lib/event-reducer";

const fetcher = vi.fn();
const approval = {
  id: "approval-a",
  toolCallId: "tool-a",
  toolName: "file_upload",
  arguments: { attachmentId: "older", instruction: "Summarize this." },
  contentRef: "private-checksum",
  actionHash: "private-action",
  deadline: "2099-01-01T00:00:00Z",
  policyRevision: 0,
  toolsetVersion: "v1",
};
const policy = { revision: 0, approvalAll: false, toolsetVersion: "v1" };
function card(a = approval, conversationId = "chat-a") {
  return (
    <ApprovalCard
      conversationId={conversationId}
      approval={a}
      policy={policy}
      busy={false}
      onDecision={() => {}}
    />
  );
}
function response(
  id = "older",
  conversationId = "chat-a",
  name = "report.txt",
  uploadNumber = 1,
) {
  return {
    ok: true,
    json: async () => ({
      attachment: { id, conversationId, name, uploadNumber },
    }),
  };
}
beforeEach(() => {
  fetcher.mockReset();
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// Reading the current picker/newest message rather than exact action target, or
// omitting stable disambiguation, makes this older-document consent test fail.
it("identifies the older of two same-name documents in consent and saved transcript after remount", async () => {
  fetcher.mockImplementation((url: string) =>
    Promise.resolve(
      response(
        url.includes("/older/") ? "older" : "newer",
        "chat-a",
        "report.txt",
        url.includes("/older/") ? 1 : 2,
      ),
    ),
  );
  const s = initialState("chat-a");
  s.messages = [
    {
      entryId: "first",
      conversationId: "chat-a",
      turnId: "turn1",
      role: "user",
      text: "First document",
      attachmentIds: ["older"],
    },
    {
      entryId: "second",
      conversationId: "chat-a",
      turnId: "turn2",
      role: "user",
      text: "Second document",
      attachmentIds: ["newer"],
    },
  ] as any;
  function view() {
    return (
      <>
        <MessageList state={s} />
        {card()}
      </>
    );
  }
  const first = render(view());
  const consent = screen.getByRole("region", { name: "Approval needed" });
  await waitFor(() =>
    expect(consent).toHaveTextContent("report.txt · Upload 1"),
  );
  expect(consent).not.toHaveTextContent("Upload 2");
  expect(
    within(consent).getByRole("button", { name: "Approve once" }),
  ).toBeEnabled();
  expect(
    first.container.querySelector('[data-message-id="first"]'),
  ).toHaveTextContent("report.txt · Upload 1");
  expect(
    first.container.querySelector('[data-message-id="second"]'),
  ).toHaveTextContent("report.txt · Upload 2");
  expect(first.container.textContent).not.toMatch(/older|newer|private-/);
  first.unmount();
  render(view());
  await waitFor(() =>
    expect(
      screen.getByRole("region", { name: "Approval needed" }),
    ).toHaveTextContent("report.txt · Upload 1"),
  );
  expect(
    fetcher.mock.calls.every(([url]) =>
      /\/attachments\/(older|newer)\/metadata$/.test(url),
    ),
  ).toBe(true);
});

it("does not grant permission while identity is loading or metadata fails; denial stays usable", async () => {
  let reject!: (error: Error) => void;
  fetcher.mockReturnValue(
    new Promise((_, no) => {
      reject = no;
    }),
  );
  render(card());
  expect(
    screen.getByRole("button", { name: "Approve once" }),
  ).toBeDisabled();
  expect(
    screen.getByRole("button", {
      name: "Approve all for this conversation",
    }),
  ).toBeDisabled();
  expect(screen.getByRole("button", { name: "Deny" })).toBeEnabled();
  await act(async () => reject(new Error("private-network-error")));
  expect(
    screen.getByText(/Cannot identify the requested attachment/),
  ).toBeVisible();
  expect(
    screen.getByRole("button", { name: "Approve once" }),
  ).toBeDisabled();
  expect(document.body).not.toHaveTextContent("private-network-error");
});

it.each(["conversation", "target", "approval"])(
  "ignores late metadata after a %s change",
  async (change) => {
    const replies: ((value: unknown) => void)[] = [];
    fetcher.mockImplementation(
      () => new Promise((resolve) => replies.push(resolve)),
    );
    const { rerender } = render(card());
    await waitFor(() => expect(replies).toHaveLength(1));
    const next = {
      ...approval,
      id: "approval-b",
      actionHash: "different-action",
      arguments: {
        ...approval.arguments,
        attachmentId: change === "target" ? "newer" : "older",
      },
    };
    const conversationId = change === "conversation" ? "chat-b" : "chat-a";
    rerender(card(next, conversationId));
    await waitFor(() => expect(replies).toHaveLength(2));
    await act(async () =>
      replies[0](response("older", "chat-a", "stale.txt")),
    );
    expect(document.body).not.toHaveTextContent("stale.txt");
    expect(
      screen.getByRole("button", { name: "Approve once" }),
    ).toBeDisabled();
    await act(async () =>
      replies[1](
        response(
          next.arguments.attachmentId,
          conversationId,
          "current.txt",
        ),
      ),
    );
    expect(document.body).toHaveTextContent("current.txt · Upload 1");
    expect(
      screen.getByRole("button", { name: "Approve once" }),
    ).toBeEnabled();
  },
);

it.each(["conversation", "target", "approval"])(
  "immediately hides already-loaded identity when %s changes",
  async (change) => {
    fetcher
      .mockResolvedValueOnce(response())
      .mockImplementation(() => new Promise(() => {}));
    const { rerender } = render(card());
    await screen.findByText("report.txt · Upload 1");
    expect(
      screen.getByRole("button", { name: "Approve once" }),
    ).toBeEnabled();
    const next = {
      ...approval,
      id: "next-approval",
      actionHash: "next-action",
      arguments: {
        ...approval.arguments,
        attachmentId: change === "target" ? "newer" : "older",
      },
    };
    rerender(card(next, change === "conversation" ? "chat-b" : "chat-a"));
    expect(
      screen.getByRole("button", { name: "Approve once" }),
    ).toBeDisabled();
    expect(document.body).not.toHaveTextContent("report.txt · Upload 1");
  },
);

it.each([
  { id: "other" },
  { conversationId: "other-chat" },
  { uploadNumber: 0 },
  { name: "" },
])("rejects mismatched or invalid metadata %j", async (override) => {
  fetcher.mockResolvedValue({
    ok: true,
    json: async () => ({
      attachment: {
        id: "older",
        conversationId: "chat-a",
        name: "report.txt",
        uploadNumber: 1,
        ...override,
      },
    }),
  });
  render(card());
  await screen.findByText(/Cannot identify the requested attachment/);
  expect(
    screen.getByRole("button", { name: "Approve once" }),
  ).toBeDisabled();
});

it("renders filenames as text without bidi/control spoofing or loading media, retaining stale/expiry fences", async () => {
  fetcher.mockResolvedValue(
    response(
      "older",
      "chat-a",
      "<img src=https://evil.example/x>\u202e\u0000.txt",
    ),
  );
  const { container, rerender } = render(card());
  await screen.findByText(/Upload 1/);
  expect(container.querySelector("img,iframe")).toBeNull();
  expect(container.textContent).toContain(
    "<img src=https://evil.example/x>.txt",
  );
  expect(container.textContent).not.toContain("\u202e");
  rerender(card({ ...approval, policyRevision: 5 }));
  expect(
    screen.getByRole("button", { name: "Approve once" }),
  ).toBeDisabled();
  rerender(card({ ...approval, deadline: "2000-01-01T00:00:00Z" }));
  expect(
    screen.getByRole("button", { name: "Approve once" }),
  ).toBeDisabled();
});
