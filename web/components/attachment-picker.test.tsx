// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { useState } from "react";
import { AttachmentPicker } from "./attachment-picker";
import type { SelectedAttachment } from "../lib/contracts";
function Picker({ initial = [] }: { initial?: SelectedAttachment[] }) {
  const [selected, change] = useState(initial);
  return (
    <AttachmentPicker
      id="a"
      selected={selected}
      onChange={change}
      disabled={false}
      onBusy={() => {}}
    />
  );
}
const fetcher = vi.fn();
beforeEach(() => {
  vi.stubGlobal("fetch", fetcher);
  fetcher.mockReset();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("validates exact byte and count limits for picker and drop before upload", async () => {
  render(<Picker />);
  fireEvent.change(screen.getByLabelText("Attach files"), {
    target: { files: [new File([new Uint8Array(1048577)], "big.txt")] },
  });
  expect(screen.getByRole("alert").textContent).toContain("1,048,576");
  fireEvent.drop(screen.getByRole("region", { name: "Attachments" }), {
    dataTransfer: {
      files: Array.from({ length: 5 }, () => new File(["x"], "n.txt")),
    },
  });
  expect(screen.getByRole("alert").textContent).toContain("four");
  expect(fetcher).not.toHaveBeenCalled();
});
it("server validation failure is actionable, preserves earlier ready files and clears accessible busy state", async () => {
  fetcher.mockResolvedValue(
    Response.json(
      { error: { code: "invalid_signature_or_unreadable_file" } },
      { status: 422 },
    ),
  );
  render(<Picker initial={[{ id: "old", name: "already.txt" }]} />);
  fireEvent.change(screen.getByLabelText("Attach files"), {
    target: { files: [new File(["not-png"], "bad.png")] },
  });
  expect(
    (screen.getByLabelText("Attach files") as HTMLInputElement).disabled,
  ).toBe(true);
  expect((await screen.findByRole("alert")).textContent).toMatch(
    /Renaming.*does not convert/,
  );
  expect(screen.getByText("already.txt")).toBeTruthy();
  expect(
    screen
      .getByRole("region", { name: "Attachments" })
      .getAttribute("aria-busy"),
  ).toBe("false");
});
it("SVG remains an inert metadata placeholder; a mislabeled HTML preview response is never rendered", async () => {
  render(
    <Picker
      initial={[
        {
          id: "svg",
          name: "<script>alert(1)</script>.svg",
          mediaType: "image/svg+xml",
        },
        { id: "png", name: "safe.png", mediaType: "image/png" },
      ]}
    />,
  );
  expect(fetcher).not.toHaveBeenCalled();
  expect(
    screen.getAllByRole("button", { name: "Preview raster" }),
  ).toHaveLength(1);
  fetcher.mockResolvedValue(
    new Response("<svg onload=alert(1) />", {
      headers: {
        "content-type": "text/html",
        "x-content-type-options": "nosniff",
      },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Preview raster" }));
  await screen.findByText("Safe raster preview unavailable.");
  expect(document.querySelector("img,iframe,object,script")).toBeNull();
});
it("ready raster preview is explicitly fetched, bounded and revoked on unmount", async () => {
  const create = vi.fn(() => "blob:safe-raster"),
    revoke = vi.fn();
  vi.stubGlobal("URL", { createObjectURL: create, revokeObjectURL: revoke });
  fetcher.mockResolvedValue(
    new Response(new Uint8Array([137, 80, 78, 71]), {
      headers: {
        "content-type": "image/png",
        "x-content-type-options": "nosniff",
      },
    }),
  );
  const view = render(
    <Picker
      initial={[{ id: "png", name: "safe.png", mediaType: "image/png" }]}
    />,
  );
  expect(fetcher).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Preview raster" }));
  expect((await screen.findByRole("img")).getAttribute("src")).toBe(
    "blob:safe-raster",
  );
  expect(fetcher.mock.calls[0][0]).toBe("/api/conversations/a/attachments/png");
  view.unmount();
  expect(revoke).toHaveBeenCalledWith("blob:safe-raster");
});
