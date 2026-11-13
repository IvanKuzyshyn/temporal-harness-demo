import type { ChatState } from "../lib/event-reducer";
import type { ContextMetadata } from "../lib/contracts";
import { SafeMarkdown, safeLink } from "./safe-markdown";
const labels: Record<string, string> = {
  queued: "Queued",
  running: "Generating",
  awaiting_approval: "Waiting for approval",
  cancelling: "Cancelling — waiting for work to join",
  cancelled: "Cancelled",
  failed: "Failed",
  completed: "Completed",
};
export function Coverage({ context }: { context: ContextMetadata }) {
  const c = context.coverage;
  const partial = c.evidence?.included?.some(
    (s) =>
      s.excerptTruncated ||
      s.sourceTruncated ||
      s.sourceError ||
      s.extractionStatus === "partial",
  );
  return (
    <div className="coverage">
      <p>
        Bounded, lossy context · {c.recentExchanges} recent exchanges
        {c.summaryVersion > 0 ? " + generated notes" : ""}
      </p>
      {(c.omittedExchanges > 0 || c.evidence || c.quotations) && (
        <p>
          {c.omittedExchanges} exchanges outside summary/recent ·{" "}
          {c.evidence?.omitted ?? 0} tool sources omitted ·{" "}
          {c.quotations?.omitted ?? 0} quotation pairs omitted
        </p>
      )}
      {partial && (
        <p className="warning">
          Partial or failed source coverage; excerpts may be clipped.
        </p>
      )}
      {c.summaryFailure && (
        <p className="warning">Reduced history: {c.summaryFailure}</p>
      )}
      {c.outputTruncated && (
        <p className="warning">
          Output limit reached — reply may be incomplete.
        </p>
      )}
      <details>
        <summary>Context provenance</summary>
        <p>
          Generated notes can misstate history. Zero omissions does not mean
          full-fidelity memory. Source IDs and hashes identify records, not
          permission to read originals.
        </p>
        <p>
          Request bounds, not measured usage: input{" "}
          {context.accounting.inputTokens}, output allocation{" "}
          {context.accounting.outputTokens}, reserve{" "}
          {context.accounting.reserve}; total {context.accounting.total}.
        </p>
        <pre>{JSON.stringify(context.coverage, null, 2)}</pre>
      </details>
    </div>
  );
}
export function WorkStatus({ state }: { state: ChatState }) {
  const m = state.metadata;
  const unresolved = Object.values(state.turns).filter(
    (t) => !["completed", "failed", "cancelled", "queued"].includes(t.status),
  );
  const queued = Object.values(state.turns).filter(
    (t) => t.status === "queued",
  );
  const rejected = Object.values(state.commands).filter(
    (c) => c.resultState === "rejected",
  );
  return (
    <section className="work-status" aria-label="Work status">
      <div className="status-line">
        <span>
          {m?.policy.approvalAll
            ? "Conversation approval enabled"
            : "Tools require approval"}
        </span>
        <span>Saved state · may lag execution</span>
      </div>
      {m && !m.dispatcherConnected && (
        <p className="warning">
          Execution connection unavailable. Accepted messages remain saved and
          queued.
        </p>
      )}
      {unresolved.map((t) => (
        <p className="live-status" key={t.id}>
          {labels[t.status] ?? t.status}
        </p>
      ))}
      {rejected.map((c) => (
        <p role="status" className="warning" key={c.commandId}>
          Command rejected: {c.result?.code ?? "rejected"}
        </p>
      ))}
      {m?.pendingApproval && (
        <aside className="approval-notice">
          <strong>Approval needed · {m.pendingApproval.toolName}</strong>
          <p>
            Work is paused. No content access has been authorized by this
            notice.
          </p>
          <pre>{JSON.stringify(m.pendingApproval.arguments, null, 2)}</pre>
          <p>
            Deadline: {m.pendingApproval.deadline}. Approval controls arrive in
            the next task.
          </p>
        </aside>
      )}
      {queued.length > 0 && (
        <details className="queued-work">
          <summary>
            {queued.length} queued{" "}
            {queued.length === 1 ? "message" : "messages"}
          </summary>
          <ol>
            {queued.map((t) => {
              const message = state.messages.find(
                (m) => m.turnId === t.id && m.role === "user",
              );
              return (
                <li key={t.id} title={t.id}>
                  {message?.text.replace(/\s+/g, " ").slice(0, 100) ||
                    `Message ${t.id}`}
                </li>
              );
            })}
          </ol>
        </details>
      )}
    </section>
  );
}
export function MessageList({ state }: { state: ChatState }) {
  return (
    <>
      {state.messages.map((m) => {
        const t =
          state.turns[m.turnId] ||
          (m.turnStatus
            ? { id: m.turnId, status: m.turnStatus, reason: m.turnReason }
            : undefined);
        return (
          <article
            key={m.entryId}
            data-message-id={m.entryId}
            className={`message ${m.role}`}
          >
            <div className="message-label">
              {m.role === "user" ? "You" : "Assistant"}
              <span>
                {m.role === "assistant"
                  ? m.completionState === "completed"
                    ? "Saved reply"
                    : "Incomplete"
                  : "Saved message"}
              </span>
            </div>
            <SafeMarkdown text={m.text} />
            {m.attachmentIds?.length ? (
              <p className="muted">
                {m.attachmentIds.length} saved attachment(s) · original access
                requires approval
              </p>
            ) : null}
            {t && t.status !== "completed" && (
              <p
                className={
                  ["failed", "cancelled"].includes(t.status)
                    ? "warning"
                    : "live-status"
                }
              >
                {labels[t.status] ?? t.status}
                {t.reason ? ` · ${t.reason}` : ""}
              </p>
            )}
            {m.context && <Coverage context={m.context} />}
          </article>
        );
      })}
      {Object.entries(state.drafts).map(([key, d]) => (
        <article key={key} className="message assistant draft">
          <div className="message-label">
            Assistant <span>Live draft · not saved · attempt {d.attempt}</span>
          </div>
          <SafeMarkdown text={d.text || "Preparing a response…"} />
          {d.bounded && (
            <p className="muted">
              Live preview bounded; the saved reply will replace it.
            </p>
          )}
        </article>
      ))}
      {Object.values(state.tools).map((t) => {
        const result = t.result;
        const url = safeLink(
          typeof result?.finalUrl === "string"
            ? result.finalUrl
            : typeof result?.url === "string"
              ? result.url
              : undefined,
        );
        return (
          <section className="tool-result" key={t.id}>
            <p>
              {t.name} · {t.status}
              {result?.truncated || result?.extractionStatus === "partial"
                ? " · Partial coverage"
                : ""}
              {result?.error ? " · Source error" : ""}
            </p>
            {url && (
              <a
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                referrerPolicy="no-referrer"
              >
                Open source ↗
              </a>
            )}
            <details>
              <summary>Work details</summary>
              <pre>{JSON.stringify(result, null, 2)}</pre>
            </details>
          </section>
        );
      })}
    </>
  );
}
