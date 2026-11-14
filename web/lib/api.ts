import type { Envelope } from "./contracts";

export class APIError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`${code} (${status})`);
    this.name = "APIError";
  }
  // Only immutable message-size validation is known to precede AcceptCommand.
  // A transport failure, unknown code, 409, or 5xx cannot release a retry ID.
  get messageCorrection(): string | undefined {
    if (this.status !== 413) return;
    if (this.code === "prompt_too_large")
      return "Message not sent: limit 65,536 UTF-8 bytes. Shorten your text, then send again.";
    if (this.code === "body_too_large")
      return "Message not sent: request body is too large. Shorten your text, then send again.";
  }
}
export async function api<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const res = await fetch("/api" + path, {
    method: body === undefined ? "GET" : "POST",
    headers:
      body === undefined ? undefined : { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
    signal,
  });
  if (!res.ok) {
    let code = "request_failed";
    try {
      const received = (await res.json()).error?.code;
      if (typeof received === "string") code = received;
    } catch {}
    throw new APIError(res.status, code);
  }
  return res.json();
}
export function watchConversation(
  id: string,
  onEvent: (e: Envelope) => void,
  onState: (state: "live" | "reconnecting") => void,
) {
  const stream = new EventSource(`/api/conversations/${id}/events`);
  let active = true;
  stream.onopen = () => {
    if (active) onState("live");
  };
  stream.onerror = () => {
    if (active) onState("reconnecting");
  };
  stream.onmessage = (message) => {
    if (!active) return;
    try {
      onEvent(JSON.parse(message.data));
    } catch {
      onState("reconnecting");
    }
  };
  return () => {
    active = false;
    stream.close();
  };
}
export function saved<T>(key: string): T | null {
  try {
    const value = localStorage.getItem(key);
    return value ? JSON.parse(value) : null;
  } catch {
    return null;
  }
}
export function save(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
    return true;
  } catch {
    return false;
  }
}
export function forget(key: string) {
  try {
    localStorage.removeItem(key);
  } catch {}
}
