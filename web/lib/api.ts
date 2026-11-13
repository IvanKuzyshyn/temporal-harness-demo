import type { Envelope } from "./contracts";
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
      code = (await res.json()).error?.code ?? code;
    } catch {}
    throw new Error(`${code} (${res.status})`);
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
