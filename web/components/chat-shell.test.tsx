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

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: () => {} }) }));
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
  model: "gemma4:e4b" as const,
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

it.each(["once", "all", "deny"])(
  "approvalOnceAndAll decision %s remains pending until durable result, then shows asynchronous rejection",
  async (decision) => {
    post = async (i) =>
      Response.json({ ...receipt(i), resultState: "pending", result: null });
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
    expect(screen.getByText(/Decision.*awaiting execution/i)).toBeTruthy();
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
    expect(screen.queryByText(/Decision applied/)).toBeNull();
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
  expect(screen.getByText(/Conversation approval enabled/)).toBeTruthy();
  view.unmount();
  await mount({ metadata });
  fireEvent.click(screen.getByRole("button", { name: "Revoke approval" }));
  await waitFor(() => expect(sent.length).toBe(1));
  expect(paths[0]).toBe("/api/conversations/a/approval-policy/revoke");
  expect(sent[0]).toEqual({ requestId: expect.any(String) });
  expect(screen.getByText(/Conversation approval enabled/)).toBeTruthy();
  await act(async () =>
    Source.snapshot([{ ...receipt(sent[0]), resultState: "accepted" }], {
      metadata: {
        ...metadata,
        policy: { ...metadata.policy, approvalAll: false, revision: 8 },
      },
    }),
  );
  expect(screen.queryByText(/Conversation approval enabled/)).toBeNull();
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
    screen.getByRole("button", { name: "Attach again image-ready" }),
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
  expect(screen.getByText("1 queued message")).toBeTruthy();
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
  "multi-file navigation retains the whole batch and late errors (failure=%s), including remount while active",
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
        files: ["one.txt", "two.txt", "three.txt", "four.txt"].map(
          (n) => new File(["abc"], n),
        ),
      },
    });
    expect(names).toEqual(["one.txt"]); // one request at a time
    view.rerender(<ChatShell conversationId="b" />);
    await act(async () => pending[0](ready("one.txt")));
    await waitFor(() => expect(names).toEqual(["one.txt", "two.txt"]));
    expect(screen.queryByText("one.txt")).toBeNull();
    expect(
      screen
        .getByRole("region", { name: "Attachments" })
        .getAttribute("aria-busy"),
    ).toBe("false");
    // Complete/fail the second file while unmounted, then reopen during file three.
    await act(async () =>
      pending[1](
        failSecond
          ? Response.json(
              { error: { code: "invalid_signature_or_unreadable_file" } },
              { status: 422 },
            )
          : ready("two.txt"),
      ),
    );
    await waitFor(() => expect(names).toHaveLength(3));
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
    expect(names).toHaveLength(3); // remount cannot start an overlapping batch
    if (failSecond)
      expect(screen.getByRole("alert").textContent).toMatch(
        /two.txt.*Renaming/s,
      );
    await act(async () => pending[2](ready("three.txt")));
    await waitFor(() => expect(names).toHaveLength(4));
    await act(async () => pending[3](ready("four.txt")));
    const storedNames = JSON.parse(
      localStorage.getItem("chat.attachments.a")!,
    ).map((a: { name: string }) => a.name);
    expect(storedNames).toEqual(
      failSecond
        ? ["one.txt", "three.txt", "four.txt"]
        : ["one.txt", "two.txt", "three.txt", "four.txt"],
    );
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
        /two.txt.*Renaming/s,
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
  expect(screen.getByText(/Stop.*admitted by execution/)).toBeTruthy();
  storage.mockRestore();
});
it("pendingApprovalAlwaysVisible outside collapsed details with exact scope and expiry", async () => {
  await mount({ metadata: busyMetadata });
  const card = screen.getByRole("region", { name: "Approval needed" });
  expect(card.closest("details")).toBeNull();
  expect(card.textContent).toContain("web_fetch@1,file_upload@1");
  expect(card.textContent).toContain("2099-01-01");
  expect(card.textContent).toContain("a".repeat(64));
  expect(screen.getByRole("button", { name: "Deny" })).toBeTruthy();
});
