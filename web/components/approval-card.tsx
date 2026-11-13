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
        <span>{approval.toolName}</span>
      </div>
      <pre className="approval-arguments" aria-label="Exact action arguments">
        {JSON.stringify(approval.arguments, null, 2)}
      </pre>
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
        Policy revision {approval.policyRevision} · {approval.toolsetVersion}
      </p>
      <p className="scope-line">Expires {approval.deadline}</p>
      <p>Uploading a file does not authorize model access.</p>
      <p className="scope-line">
        Once covers this action and its retries. All covers this version of
        web_fetch and file_upload, including child work, until revoked.
      </p>
      <details>
        <summary>Action identity</summary>
        <p>Approval {approval.id}</p>
        <p>Tool call {approval.toolCallId}</p>
        <p>Action hash {approval.actionHash}</p>
        <p>Content reference {approval.contentRef}</p>
      </details>
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
      {controls.map((c) => (
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
            {c.body.expectedActiveTurnId && (
              <span className="control-target">
                {" "}
                · target {c.body.expectedActiveTurnId}
              </span>
            )}
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
