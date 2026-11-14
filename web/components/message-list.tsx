import type { ChatState } from "../lib/event-reducer";
import { SafeMarkdown } from "./safe-markdown";
import { SavedAttachment } from "./attachment-label";
const labels: Record<string, string> = {
  queued: "Queued",
  running: "Thinking…",
  awaiting_approval: "Waiting for approval",
  cancelling: "Stopping…",
  cancelled: "Stopped",
  failed: "Failed — please try again.",
  completed: "",
};
// Durable records drive concise user outcomes, never diagnostic dumps.
export function WorkStatus({
  state,
  children,
}: {
  state: ChatState;
  children?: React.ReactNode;
}) {
  return (
    <>
      {children ??
        (state.metadata?.pendingApproval && <p>Approval needed</p>)}
      {Object.values(state.turns).some((t) => t.status === "queued") && (
        <p className="live-status">Follow-up queued</p>
      )}
      {state.metadata && !state.metadata.dispatcherConnected && (
        <p className="warning" role="status">
          Connection unavailable. Your messages are saved and waiting.
        </p>
      )}
      {Object.values(state.commands).some(
        (c) => c.resultState === "rejected",
      ) && (
        <p className="warning" role="status">
          An action could not be completed. Refresh and try again.
        </p>
      )}
    </>
  );
}
export function MessageList({
  state,
  onAttach,
  attachmentBusy,
}: {
  state: ChatState;
  onAttach?: (id: string) => void;
  attachmentBusy?: boolean;
}) {
  // One notice per failed operation kind, anchored to the latest saved message
  // of its own turn (including when no assistant answer has been persisted).
  const lastMessage = new Map(
    state.messages.map((m) => [m.turnId, m.entryId]),
  );
  const failures = new Map<string, Set<string>>();
  for (const tool of Object.values(state.tools)) {
    if (tool.status !== "failed") continue;
    const notices = failures.get(tool.turnId) ?? new Set<string>();
    notices.add(
      tool.name === "web_fetch"
        ? "A web page could not be read."
        : tool.name === "file_upload"
          ? "An attachment could not be read."
          : "An action could not be completed.",
    );
    failures.set(tool.turnId, notices);
  }
  return (
    <>
      {state.messages.map((m) => {
        const t =
          state.turns[m.turnId] ??
          (m.turnStatus ? { status: m.turnStatus } : undefined);
        return (
          <article
            key={m.entryId}
            data-message-id={m.entryId}
            className={`message ${m.role}`}
          >
            <div className="message-label">
              <span className="sr-only">
                {m.role === "user" ? "You" : "Assistant"}
              </span>
              {m.completionState === "incomplete" && (
                <span>Incomplete</span>
              )}
            </div>
            <SafeMarkdown text={m.text} />
            {!!m.attachmentIds?.length && (
              <ul
                className="saved-attachments"
                aria-label="Saved attachments"
              >
                {m.attachmentIds.map((id) => (
                  <SavedAttachment
                    key={id}
                    id={id}
                    conversationId={state.conversationId}
                    onAttach={onAttach}
                    busy={attachmentBusy}
                  />
                ))}
              </ul>
            )}
            {t && labels[t.status] && (
              <p
                className={
                  t.status === "failed" ? "warning" : "live-status"
                }
                role="status"
              >
                {labels[t.status]}
              </p>
            )}
            {lastMessage.get(m.turnId) === m.entryId &&
              [...(failures.get(m.turnId) ?? [])].map((notice) => (
                <p key={notice} className="warning" role="status">
                  {notice}
                </p>
              ))}
            {m.context?.coverage.outputTruncated && (
              <p className="warning">
                Reply reached its length limit and may be incomplete.
              </p>
            )}
          </article>
        );
      })}
      {Object.entries(state.drafts).map(([key, d]) => (
        <article key={key} className="message assistant draft">
          <span className="sr-only">Assistant</span>
          <SafeMarkdown text={d.text || "Thinking…"} />
        </article>
      ))}
    </>
  );
}
