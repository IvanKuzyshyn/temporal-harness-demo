"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  APIError,
  api,
  forget,
  save,
  saved,
  watchConversation,
} from "../lib/api";
import type {
  Conversation,
  ConversationPage,
  MessagePage,
  Model,
  Receipt,
} from "../lib/contracts";
import { initialState, mergePage, reduceEvent } from "../lib/event-reducer";
import { MessageList, WorkStatus } from "./message-list";

type Intent = { requestId: string; text: string; attachmentIds: string[] };
function Icon({ kind }: { kind: "menu" | "plus" | "send" | "chat" }) {
  return (
    <svg
      aria-hidden="true"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {kind === "plus" ? (
        <path d="M12 5v14M5 12h14" />
      ) : kind === "send" ? (
        <path d="m5 12 7-7 7 7M12 5v14" />
      ) : kind === "menu" ? (
        <path d="M4 6h16M4 12h16M4 18h16" />
      ) : (
        <path d="M5 4h14a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H8l-5 3V6a2 2 0 0 1 2-2Z" />
      )}
    </svg>
  );
}
export function ChatShell({ conversationId }: { conversationId?: string }) {
  const [collapsed, setCollapsed] = useState(false);
  const [mobile, setMobile] = useState(false);
  useEffect(() => {
    const q = window.matchMedia("(max-width:760px)");
    const update = () => setMobile(q.matches);
    update();
    q.addEventListener("change", update);
    return () => q.removeEventListener("change", update);
  }, []);
  const [sidebar, setSidebar] = useState(false),
    [list, setList] = useState<Conversation[]>([]),
    [cursor, setCursor] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    if (sidebar && mobile)
      document.querySelector<HTMLButtonElement>(".mobile-close")?.focus();
  }, [sidebar, mobile]);
  const refresh = useCallback(async (next = "") => {
    setBusy(true);
    try {
      const page = await api<ConversationPage>(
        "/conversations?limit=30" +
          (next ? "&cursor=" + encodeURIComponent(next) : ""),
      );
      setList((old) =>
        next
          ? [
              ...new Map(
                [...old, ...page.conversations].map((c) => [c.id, c]),
              ).values(),
            ]
          : page.conversations,
      );
      setCursor(page.cursor);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  const update = useCallback(
    (c: Conversation) =>
      setList((old) =>
        old.some((v) => v.id === c.id)
          ? old.map((v) => (v.id === c.id ? c : v))
          : [c, ...old],
      ),
    [],
  );
  function closeSidebar() {
    setSidebar(false);
    // Wait until React removes main's inert state before restoring focus.
    requestAnimationFrame(() =>
      document
        .querySelector<HTMLButtonElement>('[aria-label="Toggle sidebar"]')
        ?.focus(),
    );
  }
  return (
    <div className={`chat-shell ${collapsed ? "collapsed" : ""}`}>
      <a className="skip-link" href="#conversation">
        Skip to conversation
      </a>
      {sidebar && (
        <button
          className="sidebar-shade"
          aria-label="Close conversation sidebar"
          onClick={closeSidebar}
        />
      )}
      <aside
        className={`sidebar ${sidebar ? "open" : ""}`}
        aria-label="Conversations"
        onKeyDown={(e) => {
          if (e.key === "Escape" && mobile) {
            closeSidebar();
          }
        }}
      >
        <div className="brand">
          <Icon kind="chat" />
          <span>Local chat</span>
          <button
            className="icon-button mobile-close"
            aria-label="Close sidebar"
            onClick={closeSidebar}
          >
            ×
          </button>
        </div>
        <Link
          className="new-chat"
          prefetch={false}
          href="/"
          onClick={() => setSidebar(false)}
        >
          <Icon kind="plus" />
          New chat
        </Link>
        <div className="sidebar-caption">
          <span>CONVERSATIONS</span>
          <button
            onClick={() => refresh()}
            disabled={busy}
            aria-label="Refresh conversations"
          >
            ↻
          </button>
        </div>
        <nav aria-label="Conversation history">
          {list.map((c) => (
            <Link
              prefetch={false}
              className={conversationId === c.id ? "selected" : ""}
              aria-current={conversationId === c.id ? "page" : undefined}
              href={"/chat/" + c.id}
              key={c.id}
              onClick={() => setSidebar(false)}
            >
              <span>{c.title || "New conversation"}</span>
              <small>{c.model}</small>
            </Link>
          ))}
          {!list.length && !busy && !error && (
            <p className="sidebar-empty">
              Your conversations will appear here.
            </p>
          )}
          {busy && <p className="muted">Loading conversations…</p>}
          {cursor && (
            <button
              className="text-button"
              disabled={busy}
              onClick={() => refresh(cursor)}
            >
              More conversations
            </button>
          )}
        </nav>
        {error && (
          <p className="warning" role="alert">
            History unavailable: {error}
          </p>
        )}
        <footer className="sidebar-footer">
          <span className="local-dot" />
          On this machine<p>Durable history. Local models.</p>
        </footer>
      </aside>
      <main id="conversation" inert={mobile && sidebar}>
        <header className="topbar">
          <button
            className="icon-button"
            aria-label="Toggle sidebar"
            aria-expanded={mobile ? sidebar : !collapsed}
            onClick={() => {
              if (window.matchMedia("(max-width:760px)").matches)
                setSidebar((v) => !v);
              else setCollapsed((v) => !v);
            }}
          >
            <Icon kind="menu" />
          </button>
          <span className="topbar-label">A little space to think</span>
          <span className="local-label">LOCAL WORKSPACE</span>
        </header>
        {conversationId ? (
          <ChatSession
            key={conversationId}
            id={conversationId}
            onMetadata={update}
          />
        ) : (
          <NewConversation />
        )}
      </main>
    </div>
  );
}
function NewConversation() {
  const router = useRouter();
  const [model, setModel] = useState<Model>("qwen3.5:9b"),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [pending, setPending] = useState<{
      requestId: string;
      model: Model;
    } | null>(null);
  useEffect(() => {
    const p = saved<{ requestId: string; model: Model }>("chat.create");
    if (p) {
      setPending(p);
      setModel(p.model);
    }
  }, []);
  async function create() {
    const intent = pending ?? { requestId: crypto.randomUUID(), model };
    if (!save("chat.create", intent)) {
      setError(
        "Local request storage unavailable. Enable browser storage before sending.",
      );
      return;
    }
    setPending(intent);
    setBusy(true);
    setError("");
    try {
      const result = await api<{ conversation: Conversation }>(
        "/conversations",
        intent,
      );
      forget("chat.create");
      router.push("/chat/" + result.conversation.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="welcome">
      <div className="welcome-mark">
        <Icon kind="chat" />
      </div>
      <p className="eyebrow">YOUR PRIVATE WORKSPACE</p>
      <h1>What’s on your mind?</h1>
      <p className="welcome-copy">
        Start a conversation. Pick up where you left off.
        <br />
        Your history stays here, even when work takes time.
      </p>
      <div className="model-choice">
        <label htmlFor="model">Local model</label>
        <select
          id="model"
          value={model}
          disabled={!!pending || busy}
          onChange={(e) => setModel(e.target.value as Model)}
        >
          <option value="qwen3.5:9b">qwen3.5:9b</option>
          <option value="gemma4:e4b">gemma4:e4b</option>
        </select>
        <p>Fixed for this conversation. No silent fallback.</p>
      </div>
      <button className="primary" disabled={busy} onClick={create}>
        {busy
          ? "Creating…"
          : pending
            ? "Retry saved creation"
            : "Start conversation"}
        <Icon kind="plus" />
      </button>
      {error && (
        <p className="warning" role="alert">
          {error} · Retry uses the same request ID.
        </p>
      )}
      <p className="welcome-footnote">
        Models can make mistakes. Tool access always asks first.
      </p>
    </section>
  );
}
function ChatSession({
  id,
  onMetadata,
}: {
  id: string;
  onMetadata: (c: Conversation) => void;
}) {
  const [state, setState] = useState(() => initialState(id));
  const [connection, setConnection] = useState<"live" | "reconnecting">(
    "reconnecting",
  );
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState<Intent | null>(null);
  const pendingRef = useRef<Intent | null>(null);
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);
  const [paging, setPaging] = useState(false);
  const [reconnect, setReconnect] = useState(0);
  const scroll = useRef<HTMLDivElement>(null),
    atBottom = useRef(true),
    opened = useRef(false),
    pagePosition = useRef<{
      entryId?: string;
      top: number;
      scrollTop: number;
    } | null>(null),
    alive = useRef(true);
  const draftKey = "chat.draft." + id,
    pendingKey = "chat.pending." + id,
    rejectionKey = "chat.rejection." + id;
  useEffect(() => {
    alive.current = true;
    setDraft(saved<string>(draftKey) ?? "");
    const restored = saved<Intent>(pendingKey);
    pendingRef.current = restored;
    setPending(restored);
    const rejected = saved<{ status: number; code: string }>(rejectionKey);
    if (!restored && rejected)
      setError(
        new APIError(rejected.status, rejected.code).messageCorrection ?? "",
      );
    return () => {
      alive.current = false;
    };
  }, [draftKey, pendingKey, rejectionKey]);
  useEffect(
    () =>
      watchConversation(
        id,
        (e) => {
          setState((s) => reduceEvent(s, e));
        },
        setConnection,
      ),
    [id, reconnect],
  );
  useEffect(() => {
    if (state.metadata) onMetadata(state.metadata.conversation);
  }, [state.metadata, onMetadata]);
  useEffect(() => {
    if (
      pending &&
      Object.values(state.commands).some(
        (c) => c.requestId === pending.requestId,
      )
    ) {
      settlePending(pending, true);
    }
  }, [state.commands, pending, pendingKey, draft, draftKey]);
  // Stable key: one-second snapshots must not continually restart this timer.
  const pendingCommandKey = Object.values(state.commands)
    .filter((c) => c.resultState === "pending")
    .map((c) => c.commandId)
    .sort()
    .join(",");
  // Pending receipts outside the bounded snapshot window still resolve by ID.
  useEffect(() => {
    if (!pendingCommandKey) return;
    const ids = pendingCommandKey.split(",");
    const controller = new AbortController();
    const timer = setInterval(() => {
      for (const commandId of ids)
        void api<Receipt>(
          `/conversations/${id}/commands/${commandId}`,
          undefined,
          controller.signal,
        )
          .then((c) =>
            setState((s) =>
              reduceEvent(s, {
                id: s.cursor,
                conversationId: id,
                turnId: c.turnId,
                stepId: null,
                attempt: null,
                type: "command.result",
                data: c,
              }),
            ),
          )
          .catch(() => {});
    }, 2000);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, [pendingCommandKey, id]);
  const contentKey =
    state.messages.map((m) => m.entryId).join(",") +
    Object.values(state.drafts)
      .map((d) => d.text)
      .join("");
  useLayoutEffect(() => {
    const el = scroll.current;
    if (!el) return;
    if (pagePosition.current) {
      const position = pagePosition.current;
      const anchor = Array.from(
        el.querySelectorAll<HTMLElement>("[data-message-id]"),
      ).find((node) => node.dataset.messageId === position.entryId);
      el.scrollTop = anchor
        ? el.scrollTop + anchor.getBoundingClientRect().top - position.top
        : position.scrollTop;
      pagePosition.current = null;
      return;
    }
    if (state.loaded && (!opened.current || atBottom.current)) {
      el.scrollTop = el.scrollHeight;
      opened.current = true;
    }
  }, [contentKey, state.loaded, paging]);
  async function older() {
    if (!state.before || paging) return;
    const request = { before: state.before, generation: state.pageGeneration };
    setPaging(true);
    setError("");
    try {
      const page = await api<MessagePage>(
        `/conversations/${id}/messages?limit=20&before=${encodeURIComponent(request.before)}`,
      );
      if (!alive.current) return;
      const el = scroll.current;
      if (el) {
        const top = el.getBoundingClientRect().top;
        const anchor = Array.from(
          el.querySelectorAll<HTMLElement>("[data-message-id]"),
        ).find((node) => node.getBoundingClientRect().bottom > top);
        pagePosition.current = {
          entryId: anchor?.dataset.messageId,
          top: anchor?.getBoundingClientRect().top ?? top,
          scrollTop: el.scrollTop,
        };
      }
      atBottom.current = false;
      setState((s) => mergePage(s, id, page, request));
    } catch (e) {
      if (alive.current) setError((e as Error).message);
    } finally {
      if (alive.current) setPaging(false);
    }
  }
  // Both storage and UI belong to an intent in this mounted session, not merely
  // to a conversation. Snapshot ACKs can finish A before its HTTP response does.
  function settlePending(intent: Intent, clearDraft: boolean) {
    if (
      !alive.current ||
      pendingRef.current?.requestId !== intent.requestId ||
      saved<Intent>(pendingKey)?.requestId !== intent.requestId
    )
      return false;
    forget(pendingKey);
    forget(rejectionKey);
    pendingRef.current = null;
    setPending(null);
    setSending(false);
    setError("");
    if (clearDraft) {
      if (saved<string>(draftKey) === intent.text) save(draftKey, "");
      setDraft((current) => (current === intent.text ? "" : current));
    }
    return true;
  }
  async function send() {
    if (sending || (!pending && !draft.trim())) return;
    const intent = pendingRef.current ?? {
      requestId: crypto.randomUUID(),
      text: draft,
      attachmentIds: [],
    };
    if (!save(pendingKey, intent)) {
      setError("Browser storage unavailable; request was not sent.");
      return;
    }
    pendingRef.current = intent;
    setPending(intent);
    setSending(true);
    setError("");
    try {
      const receipt = await api<Receipt>(
        `/conversations/${id}/messages`,
        intent,
      );
      if (!alive.current) return;
      setState((s) =>
        reduceEvent(s, {
          id: s.cursor,
          conversationId: id,
          turnId: receipt.turnId,
          stepId: null,
          attempt: null,
          type: "command.result",
          data: receipt,
        }),
      );
      if (settlePending(intent, true)) atBottom.current = true;
    } catch (e) {
      if (!alive.current || pendingRef.current?.requestId !== intent.requestId)
        return;
      if (
        e instanceof APIError &&
        e.messageCorrection &&
        settlePending(intent, false)
      ) {
        save(rejectionKey, { status: e.status, code: e.code });
        setError(e.messageCorrection);
      } else {
        setError(
          (e as Error).message +
            " · Delivery is uncertain. Retry the saved submission, not a new message.",
        );
      }
    } finally {
      if (alive.current && pendingRef.current?.requestId === intent.requestId)
        setSending(false);
    }
  }
  const active = Object.values(state.turns).some((t) =>
    ["queued", "running", "awaiting_approval", "cancelling"].includes(t.status),
  );
  return (
    <>
      <div className="conversation-heading">
        <div>
          <h1>{state.metadata?.conversation.title || "New conversation"}</h1>
          <p>
            {state.metadata?.conversation.model ?? "Loading model…"}{" "}
            <span>· fixed model</span>
          </p>
        </div>
        <button
          className={"connection " + connection}
          onClick={() => {
            setConnection("reconnecting");
            setReconnect((n) => n + 1);
          }}
          aria-label="Reconnect live updates"
        >
          {connection === "live" ? "● Connected" : "↻ Reconnecting"}
        </button>
      </div>
      <div
        className="transcript"
        ref={scroll}
        onScroll={() => {
          const el = scroll.current;
          if (el)
            atBottom.current =
              el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        }}
      >
        <div className="reading-column">
          {state.before ? (
            <button
              className="history-button"
              onClick={older}
              disabled={paging}
            >
              {paging ? "Loading earlier messages…" : "Load earlier messages"}
            </button>
          ) : state.pageGeneration > 1 ? (
            <p className="history-start" role="status">
              Beginning of conversation
            </p>
          ) : null}
          {!state.loaded && (
            <p className="empty-note" role="status">
              Restoring saved conversation…
              {connection === "reconnecting" && " Waiting for the local API."}
            </p>
          )}
          {state.loaded && !state.messages.length && (
            <div className="empty-conversation">
              <h2>A fresh conversation.</h2>
              <p>Ask a question, explore an idea, or work through a problem.</p>
            </div>
          )}
          <MessageList state={state} />
        </div>
      </div>
      <div className="composer-area">
        <div className="reading-column">
          <WorkStatus state={state} />
          {error && (
            <p className="warning" role="alert">
              {error}
            </p>
          )}
          {pending && (
            <p className="pending-note">
              Submission saved locally ·{" "}
              {sending
                ? "sending…"
                : "awaiting confirmation. Retry keeps its identity."}
            </p>
          )}
          <form
            className="composer"
            onSubmit={(e) => {
              e.preventDefault();
              void send();
            }}
          >
            <label className="sr-only" htmlFor="prompt">
              Message
            </label>
            <textarea
              id="prompt"
              placeholder={
                active
                  ? "Add a follow-up to the queue…"
                  : "Message your local model…"
              }
              value={draft}
              disabled={!!pending}
              onChange={(e) => {
                setDraft(e.target.value);
                save(draftKey, e.target.value);
                forget(rejectionKey);
                setError("");
              }}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.shiftKey &&
                  !e.nativeEvent.isComposing
                ) {
                  e.preventDefault();
                  void send();
                }
              }}
              rows={2}
            />
            <div className="composer-bottom">
              <span>
                {active
                  ? "Follow-ups queue behind active work"
                  : "Enter to send · Shift + Enter for a new line"}
              </span>
              <button
                className="send-button"
                disabled={
                  sending || (!pending && !draft.trim()) || !state.loaded
                }
                aria-label={pending ? "Retry saved submission" : "Send message"}
                type="submit"
              >
                {pending ? "Retry" : <Icon kind="send" />}
              </button>
            </div>
          </form>
          <p className="composer-note">
            Closing this page won’t stop accepted work. Replies are final only
            when saved.
          </p>
        </div>
      </div>
    </>
  );
}
