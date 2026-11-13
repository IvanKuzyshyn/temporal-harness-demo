"use client";
import { useEffect, useRef, useState } from "react";
import type { Attachment, SelectedAttachment } from "../lib/contracts";
import { APIError, save, saved } from "../lib/api";

const accept = ".txt,.md,.markdown,.pdf,.docx,.png,.jpg,.jpeg,.svg";
const types = new Set([
  "text/plain",
  "text/markdown",
  "application/pdf",
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "image/png",
  "image/jpeg",
  "image/svg+xml",
]);
const uploadErrors: Record<string, string> = {
  file_exceeds_1048576_bytes:
    "Each file must be at most 1,048,576 bytes (1 MiB).",
  invalid_signature_or_unreadable_file:
    "File content is unsupported or unreadable. Use valid UTF-8 TXT/Markdown, text PDF, DOCX, PNG/JPEG, or a supported static SVG. Renaming a file does not convert it.",
  attachment_store_unavailable:
    "Attachment storage is unavailable. Start the configured attachment store and parser, then try again.",
  one_file_required: "Upload exactly one file per request.",
};
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
  const [busy, setBusy] = useState(false);
  const [drag, setDrag] = useState(false);
  const alive = useRef(true),
    locked = useRef(false);
  const current = useRef(selected);
  current.current = selected;
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  async function add(files: File[]) {
    if (disabled || locked.current || !files.length) return;
    if (files.length + current.current.length > 4) {
      setError(
        "At most four attachments per message. Remove a selection before adding more.",
      );
      return;
    }
    const invalid = files.find(
      (f) =>
        f.size > 1048576 ||
        (!types.has(f.type) &&
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
    locked.current = true;
    setBusy(true);
    onBusy(true);
    setError("");
    try {
      for (const file of files) {
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
          !types.has(a.mediaType) ||
          a.size > 1048576
        )
          throw new Error(
            "Upload did not return a ready attachment for this conversation.",
          );
        // Persist only under the original conversation, even after navigation.
        // Merge the current saved selection, not a stale mounted array.
        const key = "chat.attachments." + id;
        const prior = saved<SelectedAttachment[]>(key) ?? current.current;
        const next = [
          ...prior.filter((v) => v.id !== a.id),
          { id: a.id, name: a.name, mediaType: a.mediaType },
        ];
        if (next.length > 4)
          throw new Error(
            "Upload is saved, but the draft already has four attachments. Remove a selection before uploading more.",
          );
        if (!save(key, next))
          throw new Error(
            "Upload is saved, but browser storage could not retain its selection. Keep this page open and retry after restoring storage.",
          );
        window.dispatchEvent(
          new CustomEvent("chat-attachments", { detail: id }),
        );
        if (!alive.current) return;
        current.current = next;
        onChange(next);
      }
    } catch (e) {
      if (alive.current)
        setError(
          e instanceof APIError
            ? (uploadErrors[e.code] ??
                `Upload failed: ${e.message}. Try again; no model permission was granted.`)
            : (e as Error).message,
        );
    } finally {
      locked.current = false;
      if (alive.current) {
        setBusy(false);
        onBusy(false);
      }
    }
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
          {busy ? "Uploading…" : "+ Attach files"}
          <input
            type="file"
            aria-label="Attach files"
            accept={accept}
            multiple
            disabled={disabled || busy || selected.length >= 4}
            onChange={(e) => {
              void add(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
        </label>
        <span>Drop files here · 1 MiB each · up to 4</span>
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
      {busy && (
        <p role="status">
          Uploading and validating files. Wait before sending.
        </p>
      )}
      {error && (
        <p role="alert" className="warning">
          {error}
        </p>
      )}
      {selected.length > 0 && (
        <p className="scope-line">
          Ready IDs only. Model access still requires approval. Removing a
          selection does not delete originals.
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
    return <small>Metadata only · no inline preview</small>;
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
export function ImageLimits() {
  return (
    <details className="image-limits">
      <summary>Supported files & image limits</summary>
      <p>
        TXT/Markdown, embedded-text PDF, DOCX, PNG, JPEG and constrained static
        SVG. Images: at most 20 megapixels and 8192 pixels per dimension. SVG
        has no scripts, fonts, text, external resources or browser preview.
      </p>
      <p>
        Image analysis uses one 640×160 normalized raster per action;
        downsampling may lose detail. Use a printable ASCII instruction (up to
        4096 bytes, also subject to context budget); [img- and &lt;| sequences
        are unsupported. Instructions are never silently rewritten.
      </p>
      <p>
        Only the pinned Ollama 0.40.15 / MLX runtime and exact model digests are
        admitted. A mismatch requires restoring that runtime or requalification,
        not fallback. Arbitrary image quality is not qualified; known
        larger-image Qwen failures remain. No OCR or model fallback.
      </p>
    </details>
  );
}
