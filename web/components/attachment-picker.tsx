"use client";
import { useEffect, useRef, useState } from "react";
import type { SelectedAttachment } from "../lib/contracts";
import { saved } from "../lib/api";
import {
  attachmentTypes,
  MAX_ATTACHMENTS,
  singleAttachmentError,
  idleBatch,
  uploadBatch,
  uploadErrors,
  uploadFiles,
} from "../lib/attachment-batches";

const accept = ".txt,.md,.markdown,.pdf,.docx,.png,.jpg,.jpeg,.svg";
export function AttachmentPicker({
  id,
  selected,
  disabled,
  onChange,
  onBusy,
}: {
  id: string;
  selected: SelectedAttachment[];
  disabled: boolean;
  onChange: (items: SelectedAttachment[]) => void;
  onBusy: (busy: boolean) => void;
}) {
  const [error, setError] = useState("");
  const [batch, setBatch] = useState(idleBatch);
  const busy = batch.active;
  const [drag, setDrag] = useState(false);
  const current = useRef({ selected, onChange, onBusy });
  current.current = { selected, onChange, onBusy };
  useEffect(() => {
    const refresh = () => {
      const state = uploadBatch(id);
      setBatch(state);
      current.current.onBusy(state.active);
    };
    const progress = (event: Event) => {
      if ((event as CustomEvent<string>).detail === id) refresh();
    };
    const selection = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== id) return;
      const ready = saved<SelectedAttachment[]>("chat.attachments." + id);
      if (ready) current.current.onChange(ready);
    };
    refresh();
    window.addEventListener("chat-upload-batch", progress);
    window.addEventListener("chat-attachments", selection);
    return () => {
      window.removeEventListener("chat-upload-batch", progress);
      window.removeEventListener("chat-attachments", selection);
    };
  }, [id]);
  async function add(files: File[]) {
    if (disabled || !files.length) return;
    if (uploadBatch(id).active) {
      setError(
        "An upload is already in progress. Wait before choosing another file.",
      );
      return;
    }
    if (files.length + current.current.selected.length > MAX_ATTACHMENTS) {
      setError(singleAttachmentError);
      return;
    }
    const invalid = files.find(
      (f) =>
        f.size > 1048576 ||
        (!attachmentTypes.has(f.type) &&
          !/\.(txt|md|markdown|pdf|docx|png|jpe?g|svg)$/i.test(f.name)),
    );
    if (invalid) {
      setError(
        invalid.size > 1048576
          ? uploadErrors.file_exceeds_1048576_bytes
          : "Unsupported file type. Choose TXT, Markdown, PDF, DOCX, PNG, JPEG, or SVG.",
      );
      return;
    }
    setError("");
    // Only pre-dispatch storage failure can reject; running batches own all
    // subsequent progress/errors independently of this mounted presentation.
    void uploadFiles(id, files, current.current.selected).catch((e) =>
      setError((e as Error).message),
    );
  }
  return (
    <section
      className={`attachment-picker ${drag ? "drag-over" : ""}`}
      aria-label="Attachments"
      aria-busy={busy}
      onDragOver={(e) => {
        e.preventDefault();
        if (!disabled) setDrag(true);
      }}
      onDragLeave={() => setDrag(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDrag(false);
        void add(Array.from(e.dataTransfer.files));
      }}
    >
      <div className="attachment-toolbar">
        <label className="attach-button">
          <span aria-hidden="true">{busy ? "…" : "＋"}</span>
          <span className="sr-only">Attach a file</span>
          <input
            type="file"
            aria-label="Attach files"
            accept={accept}
            disabled={disabled || busy}
            onChange={(e) => {
              void add(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
        </label>
        <span className="sr-only">
          One file, up to 1 MiB. TXT, Markdown, PDF, DOCX, PNG, JPEG or SVG.
        </span>
      </div>
      {selected.length > 0 && (
        <ul className="attachment-list">
          {selected.map((a) => (
            <li key={a.id}>
              <span>{a.name ?? `Saved attachment ${a.id}`}</span>
              <button
                type="button"
                aria-label={`Remove ${a.name ?? a.id}`}
                disabled={disabled || busy}
                onClick={() => onChange(selected.filter((v) => v.id !== a.id))}
              >
                ×
              </button>
              <RasterPreview conversationId={id} attachment={a} />
            </li>
          ))}
        </ul>
      )}
      {busy && <p role="status">Uploading… Wait before sending.</p>}
      {(error || batch.errors.length > 0) && (
        <p role="alert" className="warning">
          {[error, ...batch.errors].filter(Boolean).join(" ")}
        </p>
      )}
    </section>
  );
}
// Only explicit user activation fetches a known, ready raster from this chat.
// Never fetch/render SVG/HTML or trust a filename/remote source as an image URL.
function RasterPreview({
  conversationId,
  attachment,
}: {
  conversationId: string;
  attachment: SelectedAttachment;
}) {
  const [src, setSrc] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const owned = useRef(""),
    alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      if (owned.current) URL.revokeObjectURL(owned.current);
    };
  }, []);
  if (!["image/png", "image/jpeg"].includes(attachment.mediaType ?? ""))
    return null;
  async function preview() {
    setBusy(true);
    try {
      const res = await fetch(
        `/api/conversations/${conversationId}/attachments/${attachment.id}`,
        { cache: "no-store" },
      );
      if (
        !res.ok ||
        res.headers.get("content-type") !== attachment.mediaType ||
        res.headers.get("x-content-type-options") !== "nosniff"
      )
        throw new Error("Safe raster preview unavailable.");
      const blob = await res.blob();
      if (blob.size > 1048576)
        throw new Error("Preview exceeds the file limit.");
      if (!alive.current) return;
      owned.current = URL.createObjectURL(blob);
      setSrc(owned.current);
    } catch (e) {
      if (alive.current) setError((e as Error).message);
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  return (
    <>
      {src ? (
        <img
          className="raster-preview"
          src={src}
          alt={`Preview of ${attachment.name ?? "attachment"}`}
        />
      ) : (
        <button
          type="button"
          className="text-button"
          disabled={busy}
          onClick={preview}
        >
          {busy ? "Loading preview…" : "Preview raster"}
        </button>
      )}
      {error && <small role="status">{error}</small>}
    </>
  );
}
