// @vitest-environment jsdom
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import type { ComponentProps } from "react";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { ChatShell } from "./chat-shell";
import type { Snapshot, Receipt, Message } from "../lib/contracts";

const { push } = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));
// Router delivery is external here; exercise the real shell/session/API/storage.
vi.mock("next/link", () => ({
  default: ({
    children,
    prefetch,
    ...props
  }: ComponentProps<"a"> & { prefetch?: boolean }) => (
    <a {...props}>{children}</a>
  ),
}));
const conversation = {
  id: "a",
  title: "Saved conversation",
  model: "qwen3.5:9b" as const,
  workflowId: "web-chat/a",
  lifecycle: "open" as const,
  policyRevision: 0,
  createdAt: "",
  updatedAt: "",
};
const base: Snapshot = {
  reset: true,
  messages: [],
  before: "",
  turns: [],
  commands: [],
  tools: [],
  attempts: [],
  metadata: {
    conversation,
    activeTurn: null,
    pendingApproval: null,
    policy: { approvalAll: false, revision: 0, toolsetVersion: "" },
    freshness: {
      source: "postgres_projection",
      authoritative: false,
      observedAt: "",
      workflowConfirmedAt: null,
    },
    dispatcherConnected: true,
  },
};
type Intent = { requestId: string; text: string; attachmentIds: string[] };
const pendingKey = "chat.pending.a",
  draftKey = "chat.draft.a";
const stored = () =>
  JSON.parse(localStorage.getItem(pendingKey) ?? "null") as Intent | null;
const receipt = (intent: Intent): Receipt => ({
  commandId: "command-" + intent.requestId,
  requestId: intent.requestId,
  turnId: "turn-" + intent.requestId,
  payloadDigest: "hash",
  dispatchState: "dispatched",
  resultState: "accepted",
  result: { state: "accepted" },
});
class Source {
  static all: Source[] = [];
  onmessage: ((e: { data: string }) => void) | null = null;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() {
    Source.all.push(this);
  }
  close() {}
  static snapshot(commands: Receipt[] = [], overrides: Partial<Snapshot> = {}) {
    const s = Source.all.at(-1)!;
    s.onopen?.();
    s.onmessage?.({
      data: JSON.stringify({
        id: "0",
        conversationId: "a",
        turnId: null,
        stepId: null,
        attempt: null,
        type: "snapshot",
        data: { ...base, commands, ...overrides },
      }),
    });
  }
}
let post: (intent: Intent) => Promise<Response>;
let sent: Intent[];
let paths: string[];
let upload: ((form: FormData) => Promise<Response>) | undefined;
let history: () => Promise<Response>;
let commandGet: (() => Promise<Response>) | undefined;
beforeEach(() => {
  localStorage.clear();
  push.mockReset();
  Source.all = [];
  sent = [];
  paths = [];
  upload = undefined;
  commandGet = undefined;
  vi.stubGlobal("EventSource", Source);
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      paths.push(input);
      if (init.body instanceof FormData) return upload!(init.body);
      const intent = JSON.parse(init.body as string);
      sent.push(intent);
      return post(intent);
    }
    for (const [id, name] of [["file-a", "document.txt"], ["image-ready", "text.png"]]) {
      if (input === `/api/conversations/a/attachments/${id}/metadata`)
        return Response.json({ attachment: { id, conversationId: "a", name, uploadNumber: 1 } });
    }
    if (input.includes("/commands/") && commandGet) return commandGet();
    if (input.includes("/messages?")) return history();
    if (input.startsWith("/api/conversations?"))
      return Response.json({ conversations: [conversation], cursor: "" });
    throw new Error("unexpected request " + input);
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function mount(overrides: Partial<Snapshot> = {}) {
  const view = render(<ChatShell conversationId="a" />);
  await act(async () => Source.snapshot([], overrides));
  return view;
}
async function submit(text: string) {
  fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
    target: { value: text },
  });
  fireEvent.submit(
    screen.getByRole("textbox", { name: "Message" }).closest("form")!,
  );
  await waitFor(() => expect(sent.length).toBeGreaterThan(0));
}

it("first prompt creates Qwen chat and retries uncertain submission with the same identities", async () => {
  let attempts = 0;
  post = async (i) => {
    if (paths.at(-1) === "/api/conversations")
      return Response.json({ conversation });
    if (++attempts === 1) throw new Error("offline");
    return Response.json(receipt(i));
  };
  render(<ChatShell />);
  await submit("Hello from a new chat");
  await screen.findByRole("alert");
  const first = sent.map((i) => ({ ...i }));
  expect(push).not.toHaveBeenCalled();
  fireEvent.submit(screen.getByRole("textbox").closest("form")!);
  await waitFor(() => expect(push).toHaveBeenCalledWith("/chat/a"));
  expect(sent[2]).toEqual(first[0]);
  expect(sent[3]).toEqual(first[1]);
});
it("first prompt size rejection opens the created chat with an editable retained draft", async () => {
  post = async (i) =>
    paths.at(-1) === "/api/conversations"
      ? Response.json({ conversation })
      : Response.json({ error: { code: "prompt_too_large" } }, { status: 413 });
  render(<ChatShell />);
  await submit("oversized draft fixture");
  await waitFor(() => expect(push).toHaveBeenCalledWith("/chat/a"));
  expect(JSON.parse(localStorage.getItem("chat.draft.a")!)).toBe(
    "oversized draft fixture",
  );
  expect(JSON.parse(localStorage.getItem("chat.rejection.a")!)).toEqual({
    status: 413,
    code: "prompt_too_large",
  });
  expect(localStorage.getItem("chat.create")).toBeNull();
});
it.each([false, true])(
  "restored multi-file %s intent cannot submit or replace, and keeps files recoverable",
  async (pending) => {
    const selected = [
      { id: "one", name: "one.txt" },
      { id: "two", name: "two.txt" },
    ];
    localStorage.setItem("chat.attachments.a", JSON.stringify(selected));
    localStorage.setItem(draftKey, JSON.stringify("old draft"));
    if (pending)
      localStorage.setItem(
        pendingKey,
        JSON.stringify({
          requestId: "old-request",
          text: "old draft",
          attachmentIds: ["one", "two"],
        }),
      );
    await mount({ metadata: busyMetadata });
    expect(screen.getByText("one.txt")).toBeTruthy();
    expect(screen.getByText("two.txt")).toBeTruthy();
    expect(
      screen
        .getAllByRole("alert")
        .map((e) => e.textContent)
        .join(" "),
    ).toMatch(/one attachment/);
    fireEvent.submit(screen.getByRole("textbox").closest("form")!);
    fireEvent.click(
      screen.getByRole("button", { name: "Replace current turn" }),
    );
    expect(sent).toHaveLength(0);
    expect(localStorage.getItem("chat.attachments.a")).toContain("two");
  },
);
it("history exhaustion retains a boundary landmark instead of removing the scroll footprint", async () => {
  const messages: Message[] = Array.from({ length: 25 }, (_, i) => ({
    entryId: `m${i + 1}`,
    turnId: `t${i + 1}`,
    conversationId: "a",
    role: "user",
    text: `Saved message ${i + 1}`,
    attachmentIds: [],
    logicalStep: 0,
    resultKind: "input",
    completionState: "completed",
    context: null,
    sequence: i + 1,
    createdAt: "",
  }));
  history = async () =>
    Response.json({ messages: messages.slice(0, 5), before: "" });
  await mount({ messages: messages.slice(5), before: "before6" });
  fireEvent.click(
    screen.getByRole("button", { name: "Load earlier messages" }),
  );
  expect(await screen.findByText("Beginning of conversation")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Load earlier messages" }),
  ).toBeNull();
  expect(document.querySelectorAll("[data-message-id]").length).toBe(25);
});

it.each([true, false])(
  "late A cannot clear uncertain B after snapshot ACK (remount=%s), including identical text and reload/retry identity",
  async (remount) => {
    let resolveA!: (r: Response) => void;
    post = async () => {
      if (sent.length === 1) return new Promise((r) => (resolveA = r));
      throw new TypeError("response lost");
    };
    let view = await mount();
    await submit("same text");
    const a = sent[0];
    await act(async () => Source.snapshot([receipt(a)]));
    expect(stored()).toBeNull();
    if (remount) {
      view.unmount();
      view = await mount();
    }
    await submit("same text");
    const b = sent[1];
    await screen.findByRole("button", { name: "Retry saved submission" });
    expect(stored()?.requestId).toBe(b.requestId);
    expect(b.requestId).not.toBe(a.requestId);
    await act(async () => resolveA(Response.json(receipt(a), { status: 202 })));
    expect(stored()?.requestId).toBe(b.requestId);
    expect(JSON.parse(localStorage.getItem(draftKey)!)).toBe("same text");
    view.unmount();
    await mount();
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).disabled).toBe(
      true,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Retry saved submission" }),
    );
    await waitFor(() => expect(sent.length).toBe(3));
    expect(sent[2]).toEqual(b);
    expect(stored()?.requestId).toBe(b.requestId);
  },
);
it("same-mounted snapshot acknowledgement preserves new draft on delayed POST completion", async () => {
  let resolve!: (r: Response) => void;
  post = () => new Promise((r) => (resolve = r));
  await mount();
  await submit("A");
  const a = sent[0];
  await act(async () => Source.snapshot([receipt(a)]));
  fireEvent.change(screen.getByRole("textbox"), {
    target: { value: "new text" },
  });
  await act(async () => resolve(Response.json(receipt(a), { status: 202 })));
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(
    "new text",
  );
  expect(JSON.parse(localStorage.getItem(draftKey)!)).toBe("new text");
});
it("413 prompt_too_large unlocks correction after reload, preserves text and sends a new valid identity", async () => {
  post = async () =>
    Response.json({ error: { code: "prompt_too_large" } }, { status: 413 });
  let view = await mount();
  const text = "é".repeat(33000);
  await submit(text);
  const rejected = sent[0];
  await waitFor(() => expect(stored()?.requestId).toBeUndefined());
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).disabled).toBe(
    false,
  );
  expect(screen.getByRole("alert").textContent).toMatch(
    /65,?536.*(?:UTF-8|bytes)/,
  );
  expect(screen.getByRole("alert").textContent).not.toMatch(/uncertain/);
  view.unmount();
  await mount();
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(text);
  expect(screen.getByRole("alert").textContent).toMatch(
    /65,?536.*(?:UTF-8|bytes)/,
  );
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).disabled).toBe(
    false,
  );
  post = async (intent) => Response.json(receipt(intent), { status: 202 });
  await submit("Corrected short message");
  await waitFor(() => expect(stored()).toBeNull());
  expect(sent[1].requestId).not.toBe(rejected.requestId);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("");
});
it.each([
  [503, "storage_unavailable"],
  [409, "conflict"],
  [413, "unknown_error"],
])(
  "unclassified HTTP %s %s retains the immutable retry identity",
  async (status, code) => {
    post = async () => Response.json({ error: { code } }, { status });
    let view = await mount();
    await submit("retain me");
    const intent = sent[0];
    await screen.findByRole("alert");
    expect(stored()?.requestId).toBe(intent.requestId);
    view.unmount();
    await mount();
    fireEvent.click(
      screen.getByRole("button", { name: "Retry saved submission" }),
    );
    await waitFor(() => expect(sent.length).toBe(2));
    expect(sent[1]).toEqual(intent);
  },
);

const approval = {
  id: "approval-a",
  toolCallId: "tool-a",
  toolName: "file_upload",
  arguments: {
    attachmentId: "file-a",
    instruction: "Translate visible text into English.",
  },
  contentRef: "checksum-a",
  actionHash: "a".repeat(64),
  deadline: "2099-01-01T00:00:00Z",
  policyRevision: 3,
  toolsetVersion: "web_fetch@1,file_upload@1",
};
const busyMetadata = {
  ...base.metadata,
  policy: {
    approvalAll: false,
    revision: 3,
    toolsetVersion: "web_fetch@1,file_upload@1",
  },
  activeTurn: { id: "old-turn", status: "awaiting_approval", updatedAt: "" },
  pendingApproval: approval,
};

it.each([
  ["once", "immediate"],
  ["all", "immediate"],
  ["deny", "immediate"],
  ["once", "deferred"],
  ["all", "deferred"],
  ["deny", "deferred"],
])(
  "approvalOnceAndAll decision %s remains pending until durable result, then shows asynchronous rejection (response=%s)",
  async (decision, delivery) => {
    let respond!: (response: Response) => void;
    post = async (i) => {
      if (delivery === "deferred")
        return new Promise<Response>((resolve) => (respond = resolve));
      return Response.json({
        ...receipt(i),
        resultState: "pending",
        result: null,
      });
    };
    await mount({ metadata: busyMetadata });
    const name =
      decision === "once"
        ? "Approve once"
        : decision === "all"
          ? "Approve all for this conversation"
          : "Deny";
    fireEvent.click(screen.getByRole("button", { name }));
    await waitFor(() => expect(sent.length).toBe(1));
    expect(paths[0]).toBe("/api/conversations/a/approvals/approval-a/decision");
    expect(sent[0]).toEqual({
      requestId: expect.any(String),
      actionHash: "a".repeat(64),
      decision,
    });
    expect(
      screen.getByRole("region", { name: "Approval needed" }).textContent,
    ).toContain("Translate visible text");
    if (delivery === "deferred") {
      // Dispatch is not a receipt: no clock/sleep can release this response.
      expect(screen.getByText(/Decision.*sending/)).toBeTruthy();
      expect(screen.queryByText(/Decision.*awaiting execution/i)).toBeNull();
      respond(
        Response.json({
          ...receipt(sent[0]),
          resultState: "pending",
          result: null,
        }),
      );
    }
    // Wait for receipt consumption AND its render, not just request dispatch.
    expect(
      await screen.findByText(/Decision.*awaiting execution/i),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("region", { name: "Approval needed" })
        .getAttribute("aria-busy"),
    ).toBe("true");
    expect(
      (screen.getByRole("button", { name }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.queryByText(/Decision.*admitted by execution/)).toBeNull();
    expect(screen.queryByText(/invalid_approval/)).toBeNull();
    await act(async () =>
      Source.snapshot(
        [
          {
            ...receipt(sent[0]),
            resultState: "rejected",
            result: { state: "rejected", code: "invalid_approval" },
          },
        ],
        { metadata: busyMetadata },
      ),
    );
    expect(screen.getAllByText(/invalid_approval/).length).toBeGreaterThan(0);
    expect(
      screen.getByText(/Decision.*rejected: invalid_approval/),
    ).toBeTruthy();
    expect(screen.queryByText(/Decision.*awaiting execution/i)).toBeNull();
    expect(screen.queryByText(/Decision applied/)).toBeNull();
    expect(sent).toHaveLength(1);
  },
);
it("approvalRevokeAfterReload restores policy from server, not session memory", async () => {
  const metadata = {
    ...base.metadata,
    policy: {
      approvalAll: true,
      revision: 7,
      toolsetVersion: "web_fetch@1,file_upload@1",
    },
  };
  post = async (i) =>
    Response.json({ ...receipt(i), resultState: "pending", result: null });
  const view = await mount({ metadata });
  expect(screen.getByRole("button", { name: "Revoke approval" })).toBeTruthy();
  view.unmount();
  await mount({ metadata });
  fireEvent.click(screen.getByRole("button", { name: "Revoke approval" }));
  await waitFor(() => expect(sent.length).toBe(1));
  expect(paths[0]).toBe("/api/conversations/a/approval-policy/revoke");
  expect(sent[0]).toEqual({ requestId: expect.any(String) });
  expect(screen.getByRole("button", { name: "Revoke approval" })).toBeTruthy();
  await act(async () =>
    Source.snapshot([{ ...receipt(sent[0]), resultState: "accepted" }], {
      metadata: {
        ...metadata,
        policy: { ...metadata.policy, approvalAll: false, revision: 8 },
      },
    }),
  );
  expect(screen.queryByRole("button", { name: "Revoke approval" })).toBeNull();
});
it("replaceDoesNotCancelNewerTurn preserves frozen replacement and stop target across reload", async () => {
  post = async () => {
    throw new TypeError("lost response");
  };
  let view = await mount({ metadata: busyMetadata });
  fireEvent.change(screen.getByRole("textbox"), {
    target: { value: "Correct this instead" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Replace current turn" }));
  await waitFor(() => expect(sent.length).toBe(1));
  const original = sent[0];
  expect(original).toMatchObject({
    expectedActiveTurnId: "old-turn",
    text: "Correct this instead",
    attachmentIds: [],
  });
  expect(paths[0]).toBe("/api/conversations/a/replace");
  view.unmount();
  await mount({
    metadata: {
      ...busyMetadata,
      activeTurn: { id: "new-turn", status: "running", updatedAt: "" },
    },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Retry saved submission" }),
  );
  await waitFor(() => expect(sent.length).toBe(2));
  expect(sent[1]).toEqual(original);
  expect(paths[1]).toBe("/api/conversations/a/replace");
  fireEvent.click(screen.getByRole("button", { name: "Stop current turn" }));
  await waitFor(() => expect(sent.length).toBe(3));
  const stop = sent[2];
  expect(stop).toEqual({
    requestId: expect.any(String),
    expectedActiveTurnId: "new-turn",
  });
  await act(async () =>
    Source.snapshot([], {
      metadata: {
        ...busyMetadata,
        activeTurn: { id: "newer-turn", status: "running", updatedAt: "" },
      },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Retry Stop" }));
  await waitFor(() => expect(sent.length).toBe(4));
  expect(sent[3]).toEqual(stop);
});
it("uploadAndTranslateImage only submits scoped ready attachment IDs and never approves on upload", async () => {
  upload = async (form) => {
    expect((form.get("file") as File).name).toBe("text.png");
    return Response.json(
      {
        attachment: {
          id: "image-ready",
          conversationId: "a",
          name: "text.png",
          mediaType: "image/png",
          size: 4,
          checksum: "hash",
          status: "ready",
        },
      },
      { status: 201 },
    );
  };
  post = async (i) => Response.json(receipt(i));
  await mount();
  fireEvent.change(screen.getByLabelText("Attach files"), {
    target: { files: [new File(["fake"], "text.png", { type: "image/png" })] },
  });
  await screen.findByText("text.png");
  await submit("Translate visible text into English.");
  expect(sent[0].attachmentIds).toEqual(["image-ready"]);
  expect(paths).toEqual([
    "/api/conversations/a/attachments",
    "/api/conversations/a/messages",
  ]);
  expect(screen.queryByRole("img")).toBeNull();
});
it("reopenAttachmentFollowUp uses canonical history IDs without local upload memory", async () => {
  post = async (i) => Response.json(receipt(i));
  await mount({
    messages: [
      {
        entryId: "saved-image",
        conversationId: "a",
        turnId: "image-turn",
        role: "user",
        text: "Earlier image",
        attachmentIds: ["image-ready"],
        logicalStep: 0,
        resultKind: "input",
        completionState: "completed",
        context: null,
        sequence: 1,
        createdAt: "",
      },
    ],
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "Attach again text.png · Upload 1" }),
  );
  await submit("Describe the same image.");
  expect(sent[0].attachmentIds).toEqual(["image-ready"]);
  expect(screen.queryByRole("img")).toBeNull();
});
it("queuedMessageVisible sends normal follow-up without redirecting it to replacement", async () => {
  post = async (i) => Response.json(receipt(i));
  await mount({
    metadata: busyMetadata,
    turns: [{ id: "old-turn", status: "awaiting_approval", reason: "" }],
  });
  await submit("Next question");
  expect(paths[0]).toBe("/api/conversations/a/messages");
  await act(async () =>
    Source.snapshot([receipt(sent[0])], {
      metadata: busyMetadata,
      turns: [{ id: "queued-turn", status: "queued", reason: "" }],
    }),
  );
  expect(screen.getByText("Follow-up queued")).toBeTruthy();
});
it("switchChatRetainsDraft retains ready upload identities with the original chat", async () => {
  upload = async () =>
    Response.json({
      attachment: {
        id: "file-ready",
        conversationId: "a",
        name: "notes.txt",
        mediaType: "text/plain",
        size: 3,
        checksum: "hash",
        status: "ready",
      },
    });
  const view = await mount();
  fireEvent.change(screen.getByRole("textbox"), {
    target: { value: "unsent notes" },
  });
  fireEvent.change(screen.getByLabelText("Attach files"), {
    target: { files: [new File(["abc"], "notes.txt", { type: "text/plain" })] },
  });
  await screen.findByText("notes.txt");
  view.rerender(<ChatShell conversationId="b" />);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("");
  expect(screen.queryByText("notes.txt")).toBeNull();
  view.rerender(<ChatShell conversationId="a" />);
  await act(async () => Source.snapshot());
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(
    "unsent notes",
  );
  expect(screen.getByText("notes.txt")).toBeTruthy();
});
it("late upload stays selected in its original chat after navigation without contaminating another chat", async () => {
  let finish!: (r: Response) => void;
  upload = () =>
    new Promise((r) => {
      finish = r;
    });
  const view = await mount();
  fireEvent.change(screen.getByLabelText("Attach files"), {
    target: { files: [new File(["abc"], "later.txt")] },
  });
  await waitFor(() => expect(paths.length).toBe(1));
  view.rerender(<ChatShell conversationId="b" />);
  await act(async () =>
    finish(
      Response.json({
        attachment: {
          id: "late-ready",
          conversationId: "a",
          name: "later.txt",
          mediaType: "text/plain",
          size: 3,
          checksum: "hash",
          status: "ready",
        },
      }),
    ),
  );
  expect(screen.queryByText("later.txt")).toBeNull();
  view.rerender(<ChatShell conversationId="a" />);
  await act(async () => Source.snapshot());
  expect(screen.getByText("later.txt")).toBeTruthy();
});
it.each([false, true])(
  "single-file navigation retains upload and late errors (failure=%s), including remount while active",
  async (failSecond) => {
    const pending: ((r: Response) => void)[] = [];
    const names: string[] = [];
    upload = (form) =>
      new Promise((resolve) => {
        names.push((form.get("file") as File).name);
        pending.push(resolve);
      });
    const ready = (name: string) =>
      Response.json({
        attachment: {
          id: "ready-" + name,
          conversationId: "a",
          name,
          mediaType: "text/plain",
          size: 3,
          checksum: "hash",
          status: "ready",
        },
      });
    const view = await mount();
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "batch draft" },
    });
    fireEvent.change(screen.getByLabelText("Attach files"), {
      target: {
        files: ["one.txt"].map((n) => new File(["abc"], n)),
      },
    });
    expect(names).toEqual(["one.txt"]); // one request at a time
    view.rerender(<ChatShell conversationId="b" />);
    expect(names).toEqual(["one.txt"]);
    expect(screen.queryByText("one.txt")).toBeNull();
    expect(
      screen
        .getByRole("region", { name: "Attachments" })
        .getAttribute("aria-busy"),
    ).toBe("false");
    expect(screen.queryByRole("alert")).toBeNull();
    view.rerender(<ChatShell conversationId="a" />);
    await act(async () => Source.snapshot());
    expect(
      screen
        .getByRole("region", { name: "Attachments" })
        .getAttribute("aria-busy"),
    ).toBe("true");
    expect(
      (screen.getByLabelText("Attach files") as HTMLInputElement).disabled,
    ).toBe(true);
    expect(
      (
        screen.getByRole("button", {
          name: "Send message",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    fireEvent.drop(screen.getByRole("region", { name: "Attachments" }), {
      dataTransfer: { files: [new File(["extra"], "extra.txt")] },
    });
    expect(names).toHaveLength(1); // remount cannot start an overlapping upload
    expect(screen.getByRole("alert").textContent).toMatch(/upload/i);
    view.rerender(<ChatShell conversationId="b" />);
    await act(async () =>
      pending[0](
        failSecond
          ? Response.json(
              { error: { code: "invalid_signature_or_unreadable_file" } },
              { status: 422 },
            )
          : ready("one.txt"),
      ),
    );
    view.rerender(<ChatShell conversationId="a" />);
    await act(async () => Source.snapshot());
    const storedNames = JSON.parse(
      localStorage.getItem("chat.attachments.a") ?? "[]",
    ).map((a: { name: string }) => a.name);
    expect(storedNames).toEqual(failSecond ? [] : ["one.txt"]);
    expect(
      screen
        .getByRole("region", { name: "Attachments" })
        .getAttribute("aria-busy"),
    ).toBe("false");
    expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(
      "batch draft",
    );
    view.unmount();
    await mount();
    if (failSecond)
      expect(screen.getByRole("alert").textContent).toMatch(
        /one.txt.*Renaming/s,
      );
    else expect(screen.queryByRole("alert")).toBeNull();
  },
);
it.each(["expired", "revision", "toolset"])(
  "approval %s cannot authorize a stale scope",
  async (kind) => {
    const a = {
      ...approval,
      ...(kind === "expired"
        ? { deadline: "2000-01-01T00:00:00Z" }
        : kind === "revision"
          ? { policyRevision: 2 }
          : { toolsetVersion: "old-toolset" }),
    };
    await mount({ metadata: { ...busyMetadata, pendingApproval: a } });
    for (const name of [
      "Approve once",
      "Approve all for this conversation",
      "Deny",
    ]) {
      expect(
        (screen.getByRole("button", { name }) as HTMLButtonElement).disabled,
      ).toBe(true);
    }
    expect(sent).toHaveLength(0);
  },
);
it("pending control receipt survives reload and an empty snapshot window, then lookup resolves rejection", async () => {
  post = async (i) =>
    Response.json({ ...receipt(i), resultState: "pending", result: null });
  const view = await mount({ metadata: busyMetadata });
  fireEvent.click(screen.getByRole("button", { name: "Stop current turn" }));
  await screen.findByText(/Stop.*saved.*awaiting execution/);
  const pending = sent[0];
  view.unmount();
  commandGet = async () =>
    Response.json({
      ...receipt(pending),
      resultState: "rejected",
      result: { code: "stale_target" },
    });
  await mount({ metadata: busyMetadata, commands: [] });
  expect(screen.getByText(/Stop.*saved.*awaiting execution/)).toBeTruthy();
  await waitFor(
    () => expect(screen.getByText(/Stop.*rejected: stale_target/)).toBeTruthy(),
    { timeout: 4000 },
  );
});
it("storage failure after control acceptance never claims the control was not sent", async () => {
  let finish!: (r: Response) => void;
  post = () =>
    new Promise((r) => {
      finish = r;
    });
  await mount({ metadata: busyMetadata });
  fireEvent.click(screen.getByRole("button", { name: "Stop current turn" }));
  await waitFor(() => expect(sent.length).toBe(1));
  const storage = vi
    .spyOn(Storage.prototype, "setItem")
    .mockImplementation(() => {
      throw new Error("quota");
    });
  await act(async () => finish(Response.json(receipt(sent[0]))));
  expect(screen.getByRole("alert").textContent).not.toMatch(
    /control was not sent/i,
  );
  expect(screen.queryByText(/Stop.*admitted by execution/)).toBeNull();
  storage.mockRestore();
});
it("pendingApprovalAlwaysVisible with plain-language action and no diagnostic identity", async () => {
  await mount({ metadata: busyMetadata });
  const card = screen.getByRole("region", { name: "Approval needed" });
  expect(card.closest("details")).toBeNull();
  expect(card.textContent).toContain("Translate visible text into English.");
  expect(card.textContent).not.toContain("web_fetch@1,file_upload@1");
  expect(card.textContent).not.toContain("a".repeat(64));
  expect(screen.getByRole("button", { name: "Deny" })).toBeTruthy();
});
