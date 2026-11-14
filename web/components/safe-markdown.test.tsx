import { it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { SafeMarkdown } from "./safe-markdown";
it("rejects raw HTML active SVG unsafe links and ALL automatic remote media", () => {
  const html = renderToStaticMarkup(
    <SafeMarkdown
      text={
        '# Heading\n\n**bold** [source](https://example.com/read)\n\n![remote](https://example.com/leak?secret=42)\n\n[bad](javascript:alert%281%29)\n\n<svg onload="alert(1)"><image href="https://example.com/raw" /></svg>\n<iframe src="https://example.com/frame"></iframe>'
      }
    />,
  );
  expect(html).toContain("<h1>Heading</h1>");
  expect(html).toContain("<strong>bold</strong>");
  expect(html).toContain('href="https://example.com/read"');
  expect(html).toContain("noreferrer");
  expect(html).not.toMatch(/<(img|svg|iframe|video|audio|object|embed|link)\b/);
  expect(html).not.toContain("src=");
  expect(html).not.toContain("javascript:");
  expect(html).not.toContain("prefetch");
  expect(html).toContain("remote");
});
