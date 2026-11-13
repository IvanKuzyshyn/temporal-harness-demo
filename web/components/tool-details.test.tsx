import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ToolDetails } from "./tool-details";
it.each(["cancelled", "failed"])(
  "%s file result distinguishes deliberate cancellation from analysis failure",
  (status) => {
    const html = renderToStaticMarkup(
      <ToolDetails
        tool={{
          id: "file",
          turnId: "turn",
          name: "file_upload",
          status,
          result: { error: status },
          sources: null,
        }}
      />,
    ).split("<details>")[0];
    if (status === "cancelled") {
      expect(html).toContain("Analysis cancelled");
      expect(html).not.toContain("file format");
      expect(html).not.toContain("runtime");
    } else expect(html).toContain("file format");
  },
);
it("generic persisted file failure exposes actionable image/profile guidance outside collapsed details without inventing an exact cause", () => {
  const html = renderToStaticMarkup(
    <ToolDetails
      tool={{
        id: "file",
        turnId: "turn",
        name: "file_upload",
        status: "failed",
        result: { text: "", error: "failed", truncated: false },
        sources: null,
      }}
    />,
  );
  const visible = html.split("<details>")[0];
  expect(visible).toContain("ASCII");
  expect(visible).toContain("runtime");
  expect(visible).toContain("file format");
  expect(visible).not.toContain("Translation completed");
});
