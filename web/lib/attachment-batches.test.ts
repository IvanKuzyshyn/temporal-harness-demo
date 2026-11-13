// @vitest-environment jsdom
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { uploadFiles } from "./attachment-batches";
const fetcher = vi.fn();
beforeEach(() => {
  localStorage.clear();
  fetcher.mockReset();
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => vi.unstubAllGlobals());
const file = () => new File(["hello"], "notes.txt", { type: "text/plain" });
it("rejects multiple files and an existing selection before any upload", async () => {
  await expect(uploadFiles("multi", [file(), file()], [])).rejects.toThrow(
    /one attachment/,
  );
  await expect(
    uploadFiles("selected", [file()], [{ id: "old" }]),
  ).rejects.toThrow(/one attachment/);
  expect(fetcher).not.toHaveBeenCalled();
});
it("reserves the slot synchronously and rejects a rapid second selection", async () => {
  let finish!: (r: Response) => void;
  fetcher.mockImplementation(
    () =>
      new Promise((r) => {
        finish = r;
      }),
  );
  const first = uploadFiles("rapid", [file()], []);
  await expect(uploadFiles("rapid", [file()], [])).rejects.toThrow(/upload/i);
  expect(fetcher).toHaveBeenCalledTimes(1);
  finish(
    Response.json({
      attachment: {
        id: "one",
        conversationId: "rapid",
        status: "ready",
        name: "notes.txt",
        mediaType: "text/plain",
        size: 5,
      },
    }),
  );
  await first;
  expect(JSON.parse(localStorage.getItem("chat.attachments.rapid")!)).toEqual([
    { id: "one", name: "notes.txt", mediaType: "text/plain" },
  ]);
  await expect(uploadFiles("rapid", [file()], [])).rejects.toThrow(
    /one attachment/,
  );
  expect(fetcher).toHaveBeenCalledTimes(1);
});
