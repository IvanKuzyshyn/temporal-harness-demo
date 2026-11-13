import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ToolDetails } from "./tool-details";
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
