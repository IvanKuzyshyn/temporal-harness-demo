import { APIError, save, saved } from "./api";
import type { Attachment, SelectedAttachment } from "./contracts";

export const MAX_ATTACHMENTS = 1;
export const singleAttachmentError =
  "Choose one attachment per message. Remove the current selection before adding another.";

export const attachmentTypes = new Set([
  "text/plain",
  "text/markdown",
  "application/pdf",
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "image/png",
  "image/jpeg",
  "image/svg+xml",
]);
export const uploadErrors: Record<string, string> = {
  file_exceeds_1048576_bytes:
    "Each file must be at most 1,048,576 bytes (1 MiB).",
  invalid_signature_or_unreadable_file:
    "File content is unsupported or unreadable. Use valid UTF-8 TXT/Markdown, text PDF, DOCX, PNG/JPEG, or a supported static SVG. Renaming a file does not convert it.",
  attachment_store_unavailable:
    "Attachment storage is unavailable. Start the configured attachment store and parser, then try again.",
  one_file_required: "Upload exactly one file per request.",
};
export type UploadBatch = {
  active: boolean;
  remaining: string[];
  errors: string[];
};
export const idleBatch: UploadBatch = {
  active: false,
  remaining: [],
  errors: [],
};
// A conversation, not a mounted picker, owns the sequential batch and its lock.
// Files remain only in the running closure. Durable metadata records unfinished
// names, never pretends that client bytes are a ready server attachment.
const batches = new Map<string, UploadBatch>();
const key = (id: string) => "chat.upload-batch." + id;
export function uploadBatch(id: string): UploadBatch {
  const live = batches.get(id);
  if (live) return live;
  const prior = saved<UploadBatch>(key(id));
  if (!prior) return idleBatch;
  if (!prior.active) return prior;
  // Full page reload lost the in-memory File objects. No silent auto-retry or
  // false busy/completion state; retain an explicit recoverable selection notice.
  return {
    ...prior,
    active: false,
    errors: [
      ...prior.errors,
      `Upload interrupted before confirmation: ${prior.remaining.join(", ")}. Reselect these files to retry; a prior upload may already be saved. No model permission was granted.`,
    ],
  };
}
function publish(id: string, state: UploadBatch) {
  const durable = save(key(id), state);
  if (!durable && !state.errors.some((e) => e.startsWith("Browser storage"))) {
    state = {
      ...state,
      errors: [
        ...state.errors,
        "Browser storage unavailable; upload progress/errors are retained only in this page. Keep it open and restore storage.",
      ],
    };
  }
  if (state.active || !durable) batches.set(id, state);
  else batches.delete(id);
  window.dispatchEvent(new CustomEvent("chat-upload-batch", { detail: id }));
  return state;
}
export async function uploadFiles(
  id: string,
  files: File[],
  initial: SelectedAttachment[],
) {
  if (uploadBatch(id).active)
    throw new Error(
      "An upload is already in progress. Wait before choosing another file.",
    );
  const selected =
    saved<SelectedAttachment[]>("chat.attachments." + id) ?? initial;
  if (files.length !== 1 || selected.length + files.length > MAX_ATTACHMENTS)
    throw new Error(singleAttachmentError);
  let state: UploadBatch = {
    active: true,
    remaining: files.map((f) => f.name),
    errors: [],
  };
  // Do not start an unrecorded batch when storage cannot retain its identity.
  if (!save(key(id), state))
    throw new Error(
      "Browser storage unavailable; upload batch was not started.",
    );
  state = publish(id, state);
  let selection = initial;
  for (const file of files) {
    try {
      const form = new FormData();
      form.set("file", file);
      const res = await fetch(`/api/conversations/${id}/attachments`, {
        method: "POST",
        body: form,
        cache: "no-store",
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        throw new APIError(res.status, data.error?.code ?? "upload_failed");
      }
      const { attachment: a } = (await res.json()) as {
        attachment: Attachment;
      };
      if (
        !a ||
        a.conversationId !== id ||
        a.status !== "ready" ||
        !a.id ||
        !attachmentTypes.has(a.mediaType) ||
        a.size > 1048576
      )
        throw new Error(
          "Upload did not return a ready attachment for this conversation.",
        );
      const attachmentKey = "chat.attachments." + id;
      const prior = saved<SelectedAttachment[]>(attachmentKey) ?? selection;
      const next = [
        ...prior.filter((v) => v.id !== a.id),
        { id: a.id, name: a.name, mediaType: a.mediaType },
      ];
      if (next.length > MAX_ATTACHMENTS)
        throw new Error(
          `Upload ${a.id} is saved, but the draft already has one attachment. Remove a selection before retrying.`,
        );
      if (!save(attachmentKey, next))
        throw new Error(
          `Upload ${a.id} is saved, but browser storage could not retain its selection. Restore storage before retrying.`,
        );
      selection = next;
      window.dispatchEvent(new CustomEvent("chat-attachments", { detail: id }));
    } catch (e) {
      const message =
        e instanceof APIError
          ? (uploadErrors[e.code] ??
            `Upload failed: ${e.message}. No model permission was granted.`)
          : (e as Error).message;
      state = {
        ...state,
        errors: [
          ...state.errors,
          `${file.name}: ${message} Reselect this file to retry.`,
        ],
      };
    }
    state = publish(id, { ...state, remaining: state.remaining.slice(1) });
  }
  publish(id, { ...state, active: false });
}
