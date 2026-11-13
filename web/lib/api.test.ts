import { it, expect, vi } from "vitest";
import { watchConversation } from "./api";
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
