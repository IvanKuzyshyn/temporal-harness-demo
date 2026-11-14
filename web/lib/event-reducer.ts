import type {
  Attempt,
  Envelope,
  Message,
  MessagePage,
  Metadata,
  Receipt,
  Tool,
  Turn,
} from "./contracts";
export type Draft = Attempt & { text: string; bounded?: boolean };
export type ChatState = {
  conversationId: string;
  messages: Message[];
  drafts: Record<string, Draft>;
  attempts: Record<string, Attempt>;
  commands: Record<string, Receipt>;
  turns: Record<string, Turn>;
  tools: Record<string, Tool>;
  metadata: Metadata | null;
  before: string | null;
  pageGeneration: number;
  cursor: string;
  loaded: boolean;
};
export function initialState(conversationId: string): ChatState {
  return {
    conversationId,
    messages: [],
    drafts: {},
    attempts: {},
    commands: {},
    turns: {},
    tools: {},
    metadata: null,
    before: null,
    pageGeneration: 0,
    cursor: "0",
    loaded: false,
  };
}
const key = (turn: string, step: number) => `${turn}/${step}`;
const terminal = (status: string) =>
  ["completed", "cancelled", "failed"].includes(status);
function messages(
  existing: Message[],
  incoming: Message[],
  conversationId: string,
) {
  const byId = new Map(existing.map((m) => [m.entryId, m]));
  // Persisted records are immutable; neither replay nor older page can rewrite.
  for (const m of incoming)
    if (m.conversationId === conversationId && !byId.has(m.entryId))
      byId.set(m.entryId, m);
  return [...byId.values()].sort((a, b) => a.sequence - b.sequence);
}
function receipt(old: Receipt | undefined, next: Receipt) {
  return old && old.resultState !== "pending" ? old : next;
}
function clearSettled(s: ChatState) {
  for (const [k, d] of Object.entries(s.drafts)) {
    const a = s.attempts[k];
    if (
      terminal(s.turns[d.turnId]?.status) ||
      s.messages.some(
        (m) =>
          m.turnId === d.turnId &&
          m.role === "assistant" &&
          m.resultKind === "final",
      ) ||
      (a && (a.attempt > d.attempt || a.status !== "running"))
    )
      delete s.drafts[k];
  }
}
export type PageRequest = { before: string | null; generation: number };
export function mergePage(
  s: ChatState,
  conversationId: string,
  page: MessagePage,
  request: PageRequest,
): ChatState {
  if (s.conversationId !== conversationId) return s;
  const next = {
    ...s,
    messages: messages(s.messages, page.messages, conversationId),
    // Safe canonical rows can merge even if a reset superseded this request.
    // Only the owner of the current boundary may advance/close pagination.
    before:
      request.before === s.before && request.generation === s.pageGeneration
        ? page.before
        : s.before,
    pageGeneration:
      request.before === s.before && request.generation === s.pageGeneration
        ? s.pageGeneration + 1
        : s.pageGeneration,
    drafts: { ...s.drafts },
  };
  clearSettled(next);
  return next;
}
export function reduceEvent(s: ChatState, e: Envelope): ChatState {
  if (e.conversationId !== s.conversationId) return s;
  const n = {
    ...s,
    drafts: { ...s.drafts },
    attempts: { ...s.attempts },
    commands: { ...s.commands },
    turns: { ...s.turns },
    tools: { ...s.tools },
  };
  if (e.type === "snapshot") {
    const d = e.data;
    n.messages = messages(s.messages, d.messages, s.conversationId);
    n.metadata = d.metadata;
    n.loaded = true;
    const newest = s.messages.at(-1)?.sequence ?? 0;
    const earliest = d.messages[0]?.sequence;
    if (
      n.before === null ||
      (earliest !== undefined && earliest > newest + 1)
    ) {
      n.before = d.before;
      n.pageGeneration++;
    }
    if (d.reset) n.drafts = {};
    for (const a of d.attempts) {
      const k = key(a.turnId, a.stepId);
      if (!n.attempts[k] || a.attempt >= n.attempts[k].attempt)
        n.attempts[k] = a;
    }
    for (const t of d.turns) n.turns[t.id] = t;
    for (const c of d.commands)
      n.commands[c.commandId] = receipt(n.commands[c.commandId], c);
    for (const t of d.tools) n.tools[t.id] = t;
  } else {
    // Cursor order fences duplicated/replayed text, not canonical snapshots.
    if (
      BigInt(e.id) < BigInt(s.cursor) ||
      (e.type === "message.delta" && BigInt(e.id) === BigInt(s.cursor))
    )
      return s;
    switch (e.type) {
      case "message.delta": {
        if (e.turnId === null || e.stepId === null || e.attempt === null)
          return s;
        const k = key(e.turnId, e.stepId),
          a = n.attempts[k];
        if (
          a &&
          (a.attempt > e.attempt ||
            (a.attempt === e.attempt && a.status !== "running"))
        )
          break;
        if (!n.drafts[k] && Object.keys(n.drafts).length >= 64) break;
        n.attempts[k] = {
          turnId: e.turnId,
          stepId: e.stepId,
          attempt: e.attempt,
          status: "running",
        };
        n.drafts[k] = {
          ...n.attempts[k],
          text: e.data.text.slice(0, 8000),
          bounded: e.data.bounded,
        };
        break;
      }
      case "message.persisted":
        n.messages = messages(s.messages, [e.data], s.conversationId);
        break;
      case "command.result":
        n.commands[e.data.commandId] = receipt(
          n.commands[e.data.commandId],
          e.data,
        );
        break;
      case "turn.status":
        n.turns[e.data.id] = e.data;
        break;
      case "tool.state":
        n.tools[e.data.id] = e.data;
        break;
      case "approval.state":
        if (n.metadata) n.metadata = { ...n.metadata, pendingApproval: e.data };
        break;
      case "policy.state":
        if (n.metadata) n.metadata = { ...n.metadata, policy: e.data };
        break;
    }
  }
  n.cursor = BigInt(e.id) > BigInt(s.cursor) ? e.id : s.cursor;
  clearSettled(n);
  return n;
}
