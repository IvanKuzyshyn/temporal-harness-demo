import type { ChatState } from "../lib/event-reducer";
import { SafeMarkdown } from "./safe-markdown";
const labels: Record<string, string> = {
  queued: "Queued",
  running: "Thinking…",
  awaiting_approval: "Waiting for approval",
  cancelling: "Stopping…",
  cancelled: "Stopped",
  failed: "Failed — please try again.",
  completed: "",
};
// Durable diagnostic records stay in the reducer/API, never in presentation.
export function WorkStatus({
  state,
  children,
}: {
  state: ChatState;
  children?: React.ReactNode;
}) {
  return (
    <>
      {children ?? (state.metadata?.pendingApproval && <p>Approval needed</p>)}
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
              {m.completionState === "incomplete" && <span>Incomplete</span>}
            </div>
            <SafeMarkdown text={m.text} />
            {!!m.attachmentIds?.length && (
              <ul className="saved-attachments" aria-label="Saved attachments">
                {m.attachmentIds.map((id) => (
                  <li key={id}>
                    <span>Attachment</span>
                    {onAttach && (
                      <button
                        type="button"
                        className="text-button"
                        disabled={attachmentBusy}
                        aria-label={`Attach again ${id}`}
                        onClick={() => onAttach(id)}
                      >
                        Attach again
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
            {t && labels[t.status] && (
              <p
                className={t.status === "failed" ? "warning" : "live-status"}
                role="status"
              >
                {labels[t.status]}
              </p>
            )}
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
