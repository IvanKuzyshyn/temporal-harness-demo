"use client";
import { useEffect, useState } from "react";
import { api } from "../lib/api";

type LabelState = { key: string; label?: string; failed?: boolean };
// Metadata only: never download an original or derive identity from the picker.
// Both the synchronous key fence and effect cleanup are necessary: old metadata
// must not enable consent even for one render after navigation/action changes.
export function useAttachmentLabel(
  conversationId: string,
  attachmentId: unknown,
  scope = "",
) {
  const valid = typeof attachmentId === "string" && attachmentId.length > 0;
  const key = JSON.stringify([conversationId, attachmentId, scope]);
  const [state, setState] = useState<LabelState>({ key: "" });
  useEffect(() => {
    if (!valid) return;
    const controller = new AbortController();
    let active = true;
    void api<{
      attachment: {
        id: string;
        conversationId: string;
        name: string;
        uploadNumber: number;
      };
    }>(
      `/conversations/${encodeURIComponent(conversationId)}/attachments/${encodeURIComponent(attachmentId as string)}/metadata`,
      undefined,
      controller.signal,
    )
      .then(({ attachment: a }) => {
        if (
          a.id !== attachmentId ||
          a.conversationId !== conversationId ||
          !Number.isSafeInteger(a.uploadNumber) ||
          a.uploadNumber < 1 ||
          typeof a.name !== "string"
        )
          throw new Error("Invalid attachment metadata");
        const name = a.name.replace(/[\p{Cc}\p{Cf}]/gu, "").trim();
        if (!name) throw new Error("Missing attachment name");
        if (active)
          setState({ key, label: `${name} · Upload ${a.uploadNumber}` });
      })
      .catch(() => {
        if (active) setState({ key, failed: true });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [conversationId, attachmentId, key, valid]);
  return !valid
    ? { key, failed: true }
    : state.key === key
      ? state
      : { key };
}

export function SavedAttachment({
  conversationId,
  id,
  onAttach,
  busy,
}: {
  conversationId: string;
  id: string;
  onAttach?: (id: string) => void;
  busy?: boolean;
}) {
  const { label, failed } = useAttachmentLabel(conversationId, id);
  return (
    <li>
      <span className="attachment-identity">
        {label ??
          (failed
            ? "Attachment name unavailable"
            : "Loading attachment name…")}
      </span>
      {onAttach && (
        <button
          type="button"
          className="text-button"
          disabled={busy}
          aria-label={`Attach again ${label ?? "attachment"}`}
          onClick={() => onAttach(id)}
        >
          Attach again
        </button>
      )}
    </li>
  );
}
