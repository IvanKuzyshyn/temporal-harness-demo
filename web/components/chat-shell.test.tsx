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
let history: () => Promise<Response>;
beforeEach(() => {
  localStorage.clear();
  Source.all = [];
  sent = [];
  vi.stubGlobal("EventSource", Source);
  vi.stubGlobal("matchMedia", () => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const intent = JSON.parse(init.body as string);
      sent.push(intent);
      return post(intent);
    }
    if (input.includes("/messages?")) return history();
    if (input.startsWith("/api/conversations?"))
      return Response.json({ conversations: [conversation], cursor: "" });
    throw new Error("unexpected request " + input);
  });
});
afterEach(() => {
  cleanup();
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
