"use client";
import { useEffect, useState } from "react";
import type { Metadata } from "../lib/contracts";
import type { Control } from "../lib/use-controls";

type Decision = "once" | "all" | "deny";
export function ApprovalCard({
  approval,
  policy,
  busy,
  onDecision,
}: {
  approval: NonNullable<Metadata["pendingApproval"]>;
  policy: Metadata["policy"];
  busy: boolean;
  onDecision: (decision: Decision) => void;
}) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const expired =
    !Number.isFinite(Date.parse(approval.deadline)) ||
    Date.parse(approval.deadline) <= now;
  const stale =
    approval.policyRevision !== policy.revision ||
    (policy.toolsetVersion !== "" &&
      approval.toolsetVersion !== policy.toolsetVersion);
  return (
    <section
      className="approval-card"
      aria-label="Approval needed"
      aria-busy={busy}
    >
      <div className="approval-heading">
        <strong>Approval needed</strong>
      </div>
      <p className="approval-action">
        {approval.toolName === "web_fetch"
          ? "Read this web page?"
          : "Read the selected attachment?"}
      </p>
      <p className="approval-description">
        {String(
          (approval.arguments as Record<string, unknown>)[
            approval.toolName === "web_fetch" ? "url" : "instruction"
          ] ?? "",
        )}
      </p>
      {(expired || stale) && (
        <p className="warning" role="status">
          {expired ? "Approval expired." : "Approval scope is stale."} Waiting
          for refreshed saved state; no new permission was granted.
        </p>
      )}
      <div className="action-row">
        <button
          type="button"
          className="primary"
          disabled={busy || expired || stale}
          onClick={() => onDecision("once")}
        >
          Approve once
        </button>
        <button
          type="button"
          className="secondary"
          disabled={busy || expired || stale}
          onClick={() => onDecision("all")}
          aria-label="Approve all for this conversation"
        >
          Approve all
        </button>
        <button
          type="button"
          className="text-button"
          disabled={busy || expired || stale}
          onClick={() => onDecision("deny")}
        >
          Deny
        </button>
      </div>
      <p className="scope-line">
        Approve all allows web pages and attachments in this conversation until
        revoked.
      </p>
    </section>
  );
}
export function ControlStatus({
  controls,
  sending,
  retry,
}: {
  controls: Control[];
  sending: string[];
  retry: (c: Control) => void;
}) {
  return (
    <div className="control-status" aria-live="polite">
      {controls
        .filter((c) => c.receipt?.resultState !== "accepted")
        .map((c) => (
          <div key={c.requestId}>
            <p
              className={
                c.receipt?.resultState === "rejected" || c.error
                  ? "warning"
                  : "muted"
              }
            >
              {c.label} ·{" "}
              {c.receipt?.resultState === "rejected"
                ? `rejected: ${c.receipt.result?.code ?? "rejected"}`
                : c.receipt?.resultState === "accepted"
                  ? "admitted by execution; see saved work state"
                  : c.receipt
                    ? "saved · awaiting execution"
                    : sending.includes(c.requestId)
                      ? "sending…"
                      : "delivery uncertain; original identity retained"}
              {c.error && ` · ${c.error}`}
            </p>
            {!c.receipt && (
              <button
                type="button"
                className="text-button"
                disabled={sending.includes(c.requestId)}
                onClick={() => retry(c)}
              >
                Retry {c.label}
              </button>
            )}
          </div>
        ))}
    </div>
  );
}
