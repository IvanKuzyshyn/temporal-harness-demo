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
  SelectedAttachment,
} from "../lib/contracts";
import { initialState, mergePage, reduceEvent } from "../lib/event-reducer";
import { MessageList, WorkStatus } from "./message-list";

import { ApprovalCard, ControlStatus } from "./approval-card";
import { AttachmentPicker } from "./attachment-picker";
import {
  MAX_ATTACHMENTS,
  singleAttachmentError,
  uploadBatch,
} from "../lib/attachment-batches";
import { useControls } from "../lib/use-controls";

type Intent = {
  requestId: string;
  text: string;
  attachmentIds: string[];
  expectedActiveTurnId?: string;
};
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
  const [collapsed, setCollapsed] = useState(true);
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
        .querySelector<HTMLButtonElement>(
          '.topbar [aria-label="Toggle sidebar"]',
        )
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
          <span className="brand-name">Chat</span>
          <button
            className="icon-button desktop-toggle"
            aria-label="Toggle sidebar"
            aria-expanded={!collapsed}
            onClick={() => setCollapsed((v) => !v)}
          >
            <Icon kind="menu" />
          </button>
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
          aria-label="New chat"
          prefetch={false}
          href="/"
          onClick={() => setSidebar(false)}
        >
          <Icon kind="plus" />
          <span>New chat</span>
        </Link>
        <div className="sidebar-caption">
          <span>HISTORY</span>
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
          <Icon kind="chat" />
          <span>Local chat</span>
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
          <Link
            className="icon-button header-new"
            prefetch={false}
            href="/"
            aria-label="New chat"
          >
            <Icon kind="plus" />
          </Link>
          <span className="topbar-label">Local chat</span>
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
  type Creation = {
    requestId: string;
    model: Model;
    text?: string;
    messageRequestId?: string;
  };
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState<Creation | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const busyRef = useRef(false),
    alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    setDraft(saved<string>("chat.new-draft") ?? "");
    setPending(saved<Creation>("chat.create"));
    return () => {
      alive.current = false;
    };
  }, []);
  async function create(withMessage = true) {
    if (busyRef.current || (!pending && withMessage && !draft.trim())) return;
    const intent = pending ?? {
      requestId: crypto.randomUUID(),
      model: "qwen3.5:9b",
      ...(withMessage
        ? { text: draft, messageRequestId: crypto.randomUUID() }
        : {}),
    };
    if (intent.model !== "qwen3.5:9b") {
      setError(
        "This saved request uses a disabled model. Start a new Qwen conversation.",
      );
      return;
    }
    if (!save("chat.create", intent)) {
      setError("Browser storage unavailable; request was not sent.");
      return;
    }
    busyRef.current = true;
    setPending(intent);
    setBusy(true);
    setError("");
    let createdId: string | undefined;
    try {
      const { conversation } = await api<{ conversation: Conversation }>(
        "/conversations",
        { requestId: intent.requestId, model: intent.model },
      );
      createdId = conversation.id;
      if (intent.messageRequestId) {
        await api<Receipt>(`/conversations/${conversation.id}/messages`, {
          requestId: intent.messageRequestId,
          text: intent.text,
          attachmentIds: [],
        });
      } else if (!save("chat.draft." + conversation.id, draft)) {
        throw new Error(
          "Browser storage unavailable; draft could not be saved.",
        );
      }
      // Navigation cannot clear another page's newer creation/draft.
      if (
        !alive.current ||
        saved<Creation>("chat.create")?.requestId !== intent.requestId
      )
        return;
      forget("chat.create");
      forget("chat.new-draft");
      router.push("/chat/" + conversation.id);
    } catch (e) {
      if (
        !alive.current ||
        saved<Creation>("chat.create")?.requestId !== intent.requestId
      )
        return;
      if (createdId && e instanceof APIError && e.messageCorrection) {
        // Known pre-admission size rejection: retain an editable draft rather
        // than trapping the first prompt in an immutable retry forever.
        if (
          !save("chat.draft." + createdId, intent.text ?? "") ||
          !save("chat.rejection." + createdId, {
            status: e.status,
            code: e.code,
          })
        ) {
          setError("Browser storage unavailable; draft could not be saved.");
          return;
        }
        forget("chat.create");
        forget("chat.new-draft");
        router.push("/chat/" + createdId);
      } else setError((e as Error).message + " Retry the saved submission.");
    } finally {
      busyRef.current = false;
      if (alive.current) setBusy(false);
    }
  }
  return (
    <section className="welcome">
      <div className="welcome-heading">
        <h1>What can I help with?</h1>
        <p>Ask a question, write code, or explore ideas.</p>
      </div>
      <div className="welcome-bottom reading-column">
        <div className="suggestions">
          {[
            "Explain a concept in simple terms",
            "Help me write some code",
            "Help me plan my next project",
            "Improve a piece of writing",
          ].map((text) => (
            <button
              key={text}
              disabled={!!pending || busy}
              onClick={() => {
                setDraft(text);
                save("chat.new-draft", text);
                document.getElementById("new-prompt")?.focus();
              }}
            >
              {text}
            </button>
          ))}
        </div>
        {error && (
          <p className="warning" role="alert">
            {error}
          </p>
        )}
        {pending && pending.model !== "qwen3.5:9b" && (
          <button
            className="text-button"
            onClick={() => {
              forget("chat.create");
              setPending(null);
              setError("");
            }}
          >
            Discard disabled-model request
          </button>
        )}
        <form
          className="composer"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <label className="sr-only" htmlFor="new-prompt">
            Message
          </label>
          <textarea
            id="new-prompt"
            placeholder="Ask anything…"
            value={pending?.text ?? draft}
            disabled={!!pending || busy}
            onChange={(e) => {
              setDraft(e.target.value);
              save("chat.new-draft", e.target.value);
            }}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing
              ) {
                e.preventDefault();
                void create();
              }
            }}
          />
          <div className="composer-bottom">
            <button
              type="button"
              className="icon-button"
              aria-label="Start conversation"
              title="Start a conversation to attach a file"
              disabled={busy || !!pending}
              onClick={() => void create(false)}
            >
              <Icon kind="plus" />
            </button>
            <span className="model-label">qwen3.5:9b</span>
            <button
              className="send-button"
              type="submit"
              aria-label={pending ? "Retry saved submission" : "Send message"}
              disabled={busy || (!pending && !draft.trim())}
            >
              {pending ? "Retry" : <Icon kind="send" />}
            </button>
          </div>
        </form>
      </div>
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
  const [attachments, setAttachments] = useState<SelectedAttachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const attachmentKey = "chat.attachments." + id;
  const attachmentsRef = useRef<SelectedAttachment[]>([]);
  function selectAttachments(items: SelectedAttachment[]) {
    if (!save(attachmentKey, items)) {
      setError(
        "Browser storage unavailable; attachment selection could not be saved.",
      );
      return;
    }
    attachmentsRef.current = items;
    setAttachments(items);
  }
  const recordReceipt = useCallback(
    (receipt: Receipt) =>
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
      ),
    [id],
  );
  const controls = useControls(id, state.commands, recordReceipt);
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
    const selected = saved<SelectedAttachment[]>(attachmentKey) ?? [];
    attachmentsRef.current = selected;
    setAttachments(selected);
    const restored = saved<Intent>(pendingKey);
    if (
      selected.length > MAX_ATTACHMENTS ||
      (restored?.attachmentIds.length ?? 0) > MAX_ATTACHMENTS
    )
      setError(singleAttachmentError);
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
  useEffect(() => {
    const changed = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== id) return;
      const selected = saved<SelectedAttachment[]>(attachmentKey) ?? [];
      attachmentsRef.current = selected;
      setAttachments(selected);
    };
    window.addEventListener("chat-attachments", changed);
    return () => window.removeEventListener("chat-attachments", changed);
  }, [id, attachmentKey]);
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
      const receipt = Object.values(state.commands).find(
        (c) => c.requestId === pending.requestId,
      )!;
      settlePending(pending, receipt.resultState !== "rejected");
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
      selectAttachments(
        attachmentsRef.current.filter(
          (a) => !intent.attachmentIds.includes(a.id),
        ),
      );
    }
    return true;
  }
  async function send(replace = false) {
    if (
      sending ||
      uploading ||
      uploadBatch(id).active ||
      !state.loaded ||
      (!pending && !draft.trim() && !attachments.length)
    )
      return;
    if (
      attachmentsRef.current.length > MAX_ATTACHMENTS ||
      (pendingRef.current?.attachmentIds.length ?? 0) > MAX_ATTACHMENTS
    ) {
      setError(singleAttachmentError);
      return;
    }
    const target = state.metadata?.activeTurn;
    if (replace && (!target || target.status === "cancelling")) return;
    const intent = pendingRef.current ?? {
      requestId: crypto.randomUUID(),
      text: draft,
      attachmentIds: attachments.map((a) => a.id),
      ...(replace ? { expectedActiveTurnId: target!.id } : {}),
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
        `/conversations/${id}/${intent.expectedActiveTurnId ? "replace" : "messages"}`,
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
      if (settlePending(intent, receipt.resultState !== "rejected"))
        atBottom.current = true;
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
          <p>{state.metadata?.conversation.model ?? "Loading…"}</p>
        </div>
        {connection === "reconnecting" && (
          <button
            className={"connection " + connection}
            onClick={() => {
              setConnection("reconnecting");
              setReconnect((n) => n + 1);
            }}
            aria-label="Reconnect live updates"
          >
            Reconnecting…
          </button>
        )}
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
              <h2>What can I help with?</h2>
              <p>Ask a question, write code, or explore ideas.</p>
            </div>
          )}
          <MessageList
            state={state}
            attachmentBusy={!!pending || uploading}
            onAttach={(attachmentId) => {
              if (uploadBatch(id).active) {
                setError("Wait for the current upload.");
                return;
              }
              if (attachmentsRef.current.some((a) => a.id === attachmentId))
                return;
              if (attachmentsRef.current.length >= MAX_ATTACHMENTS) {
                setError(singleAttachmentError);
                return;
              }
              selectAttachments([
                ...attachmentsRef.current,
                { id: attachmentId },
              ]);
            }}
          />
        </div>
      </div>
      <div className="composer-area">
        <div className="reading-column">
          <div className="interaction-panel">
            <WorkStatus state={state}>
              {state.metadata?.policy.approvalAll && (
                <div className="policy-control">
                  <button
                    type="button"
                    className="text-button"
                    disabled={controls.pending("Revoke")}
                    onClick={() =>
                      controls.submit("Revoke", "approval-policy/revoke", {})
                    }
                  >
                    Revoke approval
                  </button>
                </div>
              )}
              {state.metadata?.pendingApproval && (
                <ApprovalCard
                  approval={state.metadata.pendingApproval}
                  policy={state.metadata.policy}
                  busy={controls.pending("Decision")}
                  onDecision={(decision) => {
                    const a = state.metadata!.pendingApproval!;
                    controls.submit("Decision", `approvals/${a.id}/decision`, {
                      actionHash: a.actionHash,
                      decision,
                    });
                  }}
                />
              )}
            </WorkStatus>
            <ControlStatus
              controls={controls.controls}
              sending={controls.sending}
              retry={controls.retry}
            />
            {controls.storageError && (
              <p className="warning" role="alert">
                {controls.storageError}
              </p>
            )}
          </div>
          {error && (
            <p className="warning" role="alert">
              {error}
            </p>
          )}
          {pending && pending.attachmentIds.length > MAX_ATTACHMENTS && (
            <button
              type="button"
              className="text-button"
              onClick={() => settlePending(pending, false)}
            >
              Edit saved submission
            </button>
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
                active ? "Add a follow-up to the queue…" : "Ask anything…"
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
            <AttachmentPicker
              id={id}
              selected={attachments}
              disabled={!!pending}
              onChange={selectAttachments}
              onBusy={setUploading}
            />
            <div className="composer-bottom">
              <span className="model-label">
                {state.metadata?.conversation.model ?? "Qwen"}
              </span>
              <button
                className="send-button"
                disabled={
                  sending ||
                  uploading ||
                  (!pending && !draft.trim() && !attachments.length) ||
                  !state.loaded
                }
                aria-label={pending ? "Retry saved submission" : "Send message"}
                type="submit"
              >
                {pending ? "Retry" : <Icon kind="send" />}
              </button>
            </div>
          </form>
          {state.metadata?.activeTurn && (
            <div className="turn-controls">
              <button
                type="button"
                className="secondary"
                disabled={
                  controls.pending("Stop") ||
                  state.metadata.activeTurn.status === "cancelling"
                }
                onClick={() =>
                  controls.submit("Stop", "stop", {
                    expectedActiveTurnId: state.metadata!.activeTurn!.id,
                  })
                }
              >
                Stop current turn
              </button>
              <button
                type="button"
                className="secondary"
                disabled={
                  !!pending ||
                  sending ||
                  uploading ||
                  (!draft.trim() && !attachments.length) ||
                  state.metadata.activeTurn.status === "cancelling"
                }
                onClick={() => void send(true)}
              >
                Replace current turn
              </button>
            </div>
          )}
        </div>
      </div>
    </>
  );
}
