import { it, expect, vi } from "vitest";
import { api, watchConversation } from "./api";
it("preserves structured status/code for a definitive HTTP rejection", async () => {
  vi.stubGlobal("fetch", async () =>
    Response.json({ error: { code: "prompt_too_large" } }, { status: 413 }),
  );
  try {
    await expect(
      api("/conversations/a/messages", { text: "too large" }),
    ).rejects.toMatchObject({ status: 413, code: "prompt_too_large" });
  } finally {
    vi.unstubAllGlobals();
  }
});
it("a closed stream cannot deliver queued callbacks into a replacement reader", () => {
  let source: {
    onmessage: (e: { data: string }) => void;
    onopen: () => void;
    onerror: () => void;
    close: () => void;
  };
  class FakeSource {
    onmessage = (_e: { data: string }) => {};
    onopen = () => {};
    onerror = () => {};
    close = vi.fn();
    constructor() {
      source = this;
    }
  }
  vi.stubGlobal("EventSource", FakeSource);
  try {
    const event = vi.fn(),
      status = vi.fn();
    const close = watchConversation("a", event, status);
    source!.onmessage({ data: '{"conversationId":"a"}' });
    expect(event).toHaveBeenCalledTimes(1);
    close();
    source!.onmessage({ data: '{"conversationId":"a"}' });
    source!.onopen();
    source!.onerror();
    expect(event).toHaveBeenCalledTimes(1);
    expect(status).not.toHaveBeenCalled();
    expect(source!.close).toHaveBeenCalledOnce();
  } finally {
    vi.unstubAllGlobals();
  }
});
