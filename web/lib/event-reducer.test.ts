import { describe, it, expect } from "vitest";
import { initialState, reduceEvent, mergePage } from "./event-reducer";
import type { Envelope, Message, Snapshot, Receipt } from "./contracts";
const message: Message = {
  entryId: "m1",
  conversationId: "a",
  turnId: "t",
  logicalStep: 0,
  role: "assistant",
  text: "durable",
  sequence: 2,
  resultKind: "final",
  completionState: "completed",
  context: null,
  attachmentIds: [],
  createdAt: "",
};
const receipt: Receipt = {
  commandId: "cmd",
  turnId: "t",
  resultState: "accepted",
  dispatchState: "dispatched",
  payloadDigest: "hash",
  result: { state: "accepted" },
};
const snapshot = {
  reset: true,
  messages: [message],
  before: "older",
  turns: [{ id: "t", status: "completed", reason: "" }],
  commands: [receipt],
  tools: [],
  attempts: [],
  metadata: {
    conversation: {
      id: "a",
      title: "Hello",
      model: "qwen3.5:9b",
      workflowId: "web-chat/a",
      lifecycle: "open",
      policyRevision: 0,
      createdAt: "",
      updatedAt: "",
    },
    activeTurn: null,
    pendingApproval: null,
    policy: { approvalAll: false, revision: 0, toolsetVersion: "" },
    freshness: {
      authoritative: false,
      source: "postgres_projection",
      observedAt: "",
      workflowConfirmedAt: null,
    },
    dispatcherConnected: true,
  },
} as Snapshot;
const delta = (attempt = 1, id = "1", conversationId = "a"): Envelope => ({
  id,
  conversationId,
  turnId: "t",
  stepId: 0,
  attempt,
  type: "message.delta",
  data: { text: attempt === 1 ? "old" : "retry" },
});
const snap = (data = snapshot, id = "2"): Envelope => ({
  id,
  conversationId: "a",
  turnId: null,
  stepId: null,
  attempt: null,
  type: "snapshot",
  data,
});
describe("canonical reconciliation", () => {
  it("testStaleConversationEventIgnored", () => {
    const s = initialState("b");
    expect(reduceEvent(s, delta())).toEqual(s);
    expect(mergePage(s, "a", { messages: [message], before: "" })).toEqual(s);
  });
  it("testRetryDraftReplaced", () => {
    let s = reduceEvent(initialState("a"), delta());
    s = reduceEvent(s, delta(2, "2"));
    expect(Object.values(s.drafts).map((d) => d.text)).toEqual(["retry"]);
    expect(reduceEvent(s, delta(1, "3")).drafts).toEqual(s.drafts);
  });
  it("testPersistedMessageDeduplicatesLiveDraft", () => {
    let s = reduceEvent(initialState("a"), delta());
    s = reduceEvent(s, {
      ...delta(1, "2"),
      type: "message.persisted",
      data: message,
    });
    s = reduceEvent(s, {
      ...delta(1, "3"),
      type: "message.persisted",
      data: message,
    });
    expect(s.messages).toEqual([message]);
    expect(Object.values(s.drafts)).toHaveLength(0);
    expect(Object.values(reduceEvent(s, delta(2, "4")).drafts)).toHaveLength(0);
  });
  it("testSnapshotRestoresWithoutTokenEvents", () => {
    const s = reduceEvent(initialState("a"), snap());
    expect(s.messages).toEqual([message]);
    expect(s.commands.cmd.resultState).toBe("accepted");
    expect(s.turns.t.status).toBe("completed");
    expect(s.before).toBe("older");
  });
  it("paging and reset keep older canonical records and accepted receipts", () => {
    let s = reduceEvent(initialState("a"), snap());
    const older = { ...message, entryId: "m0", turnId: "old", sequence: 1 };
    s = mergePage(s, "a", { messages: [older, message], before: "" });
    s = reduceEvent(
      s,
      snap({ ...snapshot, commands: [], before: "older" }, "4"),
    );
    expect(s.messages.map((m) => m.entryId)).toEqual(["m0", "m1"]);
    expect(s.before).toBe("");
    expect(s.commands.cmd).toEqual(receipt);
  });
  it("canonical lifecycle changes may share the snapshot watermark", () => {
    const s = reduceEvent(initialState("a"), snap());
    const next = reduceEvent(s, {
      ...delta(1, "2"),
      type: "command.result",
      data: { ...receipt, commandId: "new-command" },
    });
    expect(next.commands["new-command"]).toEqual({
      ...receipt,
      commandId: "new-command",
    });
  });
  it("a long disconnect reopens paging for the missing middle without deleting loaded history", () => {
    let s = reduceEvent(initialState("a"), snap({ ...snapshot, before: "" }));
    s = reduceEvent(
      s,
      snap(
        {
          ...snapshot,
          messages: [
            { ...message, entryId: "m30", turnId: "later", sequence: 30 },
          ],
          before: "gap",
        },
        "30",
      ),
    );
    expect(s.messages.map((m) => m.entryId)).toEqual(["m1", "m30"]);
    expect(s.before).toBe("gap");
  });
  it("snapshot attempt fences late drafts and completed steps", () => {
    const s = reduceEvent(
      initialState("a"),
      snap({
        ...snapshot,
        messages: [],
        turns: [],
        attempts: [{ turnId: "t", stepId: 0, attempt: 2, status: "running" }],
      }),
    );
    expect(Object.values(reduceEvent(s, delta(1, "3")).drafts)).toHaveLength(0);
  });
});
