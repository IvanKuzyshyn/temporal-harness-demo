// Server route only. Never import configuration or this module into a client component.
export type ProxyConfig = { origin: string; upstream: string; secret: string };
const responseHeaders = {
  "cache-control": "no-store",
  "x-content-type-options": "nosniff",
};
const failure = (status: number, code: string) =>
  Response.json({ error: { code } }, { status, headers: responseHeaders });
export async function proxy(
  request: Request,
  path: string[],
  config: ProxyConfig,
  fetcher: typeof fetch = fetch,
): Promise<Response> {
  let origin: URL, upstream: URL;
  try {
    origin = new URL(config.origin);
    upstream = new URL(config.upstream);
  } catch {
    return failure(503, "upstream_unavailable");
  }
  if (
    !["localhost", "127.0.0.1", "[::1]"].includes(origin.hostname) ||
    !["127.0.0.1", "[::1]"].includes(upstream.hostname) ||
    !["http:", "https:"].includes(upstream.protocol) ||
    !config.secret
  )
    return failure(503, "upstream_unavailable");
  const h = request.headers,
    site = h.get("sec-fetch-site"),
    mode = h.get("sec-fetch-mode"),
    supplied = h.get("origin");
  if (
    h.get("host") !== origin.host ||
    (supplied !== null && supplied !== origin.origin) ||
    (request.method !== "GET" && supplied !== origin.origin) ||
    (site !== null && site !== "same-origin") ||
    (mode !== null && !["cors", "same-origin"].includes(mode))
  )
    return failure(403, "forbidden");
  if (!["GET", "POST"].includes(request.method))
    return failure(405, "method_not_allowed");
  if (!path.length || path.some((p) => !p || !/^[-a-zA-Z0-9]+$/.test(p)))
    return failure(400, "invalid_request");
  const url = new URL("/" + path.join("/"), upstream);
  url.search = new URL(request.url).search;
  const headers = new Headers({ "X-Webchat-Secret": config.secret });
  for (const name of ["content-type", "last-event-id"]) {
    const v = h.get(name);
    if (v !== null) headers.set(name, v);
  }
  const abort = new AbortController();
  const onAbort = () => abort.abort();
  request.signal.addEventListener("abort", onAbort, { once: true });
  if (request.signal.aborted) abort.abort();
  const clean = () => request.signal.removeEventListener("abort", onAbort);
  // Only connection/headers deadline here; stream body is bounded by Go writes
  // and downstream cancellation. No cancellation endpoint is ever submitted.
  const timer = setTimeout(() => abort.abort(), 10000);
  try {
    const init: RequestInit & { duplex?: "half" } = {
      method: request.method,
      headers,
      signal: abort.signal,
      redirect: "manual",
      cache: "no-store",
    };
    if (request.method === "POST") {
      init.body = request.body;
      init.duplex = "half";
    }
    const res = await fetcher(url.toString(), init);
    clearTimeout(timer);
    if (res.status >= 300 && res.status < 400) {
      abort.abort();
      clean();
      return failure(502, "upstream_unavailable");
    }
    const out = new Headers(responseHeaders);
    out.set("x-accel-buffering", "no");
    for (const name of ["content-type", "content-disposition"]) {
      const v = res.headers.get(name);
      if (v) out.set(name, v);
    }
    const location = res.headers.get("location");
    if (location?.startsWith("/conversations/"))
      out.set("location", "/api" + location);
    if (!res.body) {
      clean();
      return new Response(null, { status: res.status, headers: out });
    }
    const reader = res.body.getReader();
    const body = new ReadableStream<Uint8Array>(
      {
        async pull(controller) {
          try {
            const { done, value } = await reader.read();
            if (done) {
              clean();
              controller.close();
            } else controller.enqueue(value);
          } catch {
            clean();
            controller.error(new Error("stream disconnected"));
          }
        },
        async cancel() {
          abort.abort();
          clean();
          await reader.cancel().catch(() => {});
        },
      },
      { highWaterMark: 0 },
    );
    return new Response(body, { status: res.status, headers: out });
  } catch {
    clearTimeout(timer);
    abort.abort();
    clean();
    return failure(503, "upstream_unavailable");
  }
}
