import type { Tool } from "../lib/contracts";
import { safeLink } from "./safe-markdown";
export function ToolDetails({ tool: t }: { tool: Tool }) {
  const result = t.result;
  const url = safeLink(
    typeof result?.finalUrl === "string"
      ? result.finalUrl
      : typeof result?.url === "string"
        ? result.url
        : undefined,
  );
  const error = typeof result?.error === "string" ? result.error : "";
  return (
    <section className="tool-result">
      <p>
        {t.name} · {t.status}
        {result?.truncated || result?.extractionStatus === "partial"
          ? " · Partial coverage"
          : ""}
        {error ? ` · Source error: ${error}` : ""}
      </p>
      {t.name === "file_upload" &&
        (t.status === "failed" ||
          (error && error !== "denied" && error !== "expired")) && (
          <p className="warning">
            Attachment analysis did not complete. Check the file format and
            original instruction. Images require the narrow 640×160 profile,
            bounded ASCII instructions and the pinned runtime/model; a mismatch
            needs the supported runtime, not fallback. See Supported files &amp;
            image limits below. No translation or rewritten action is claimed by
            this error.
          </p>
        )}
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
}
