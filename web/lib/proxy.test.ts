import { it, expect, vi } from "vitest";
import { proxy } from "./proxy";
const cfg = {
  origin: "http://localhost:13000",
  upstream: "http://127.0.0.1:18081",
  secret: "private-test-secret",
};
const request = (headers: Record<string, string> = {}, method = "GET") =>
  new Request("http://localhost:13000/api/models", {
    method,
    headers: { host: "localhost:13000", ...headers },
  });
it("rejects hostile Host Origin duplicate fetch context and originless mutations before upstream", async () => {
  const fetcher = vi.fn();
  for (const h of [
    { host: "evil.test" },
    { origin: "null" },
    { "sec-fetch-site": "cross-site" },
    { "sec-fetch-site": "same-origin, cross-site" },
    { "sec-fetch-mode": "navigate" },
  ] as Record<string, string>[])
    expect((await proxy(request(h), ["models"], cfg, fetcher)).status).toBe(
      403,
    );
  expect(
    (await proxy(request({}, "POST"), ["conversations"], cfg, fetcher)).status,
  ).toBe(403);
  expect(fetcher).not.toHaveBeenCalled();
});
it("streams first chunk without waiting for end, constructs private headers, cancels upstream reader only", async () => {
  let cancelled = false;
  const upstream = new ReadableStream({
    start(c) {
      c.enqueue(new TextEncoder().encode("data: first\n\n"));
    },
    cancel() {
      cancelled = true;
    },
  });
  const fetcher = vi.fn(
    async () =>
      new Response(upstream, {
        headers: {
          "content-type": "text/event-stream",
          "x-webchat-secret": "must-not-leak",
        },
      }),
  );
  const res = await proxy(
    request({ authorization: "evil", cookie: "evil", "last-event-id": "7" }),
    ["conversations", "abc", "events"],
    cfg,
    fetcher,
  );
  const [url, options] = fetcher.mock.calls[0] as unknown as [
    string,
    RequestInit,
  ];
  expect(url).toBe(cfg.upstream + "/conversations/abc/events");
  const headers = new Headers(options.headers);
  expect(headers.get("x-webchat-secret")).toBe(cfg.secret);
  expect(headers.get("cookie")).toBeNull();
  expect(headers.get("authorization")).toBeNull();
  expect(headers.get("last-event-id")).toBe("7");
  expect(res.headers.get("x-webchat-secret")).toBeNull();
  expect(res.headers.get("x-accel-buffering")).toBe("no");
  const reader = res.body!.getReader();
  expect(new TextDecoder().decode((await reader.read()).value)).toBe(
    "data: first\n\n",
  );
  await reader.cancel();
  expect(cancelled).toBe(true);
  expect(options.signal?.aborted).toBe(true);
});
