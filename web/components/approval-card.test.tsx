import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ApprovalCard } from "./approval-card";
it("shows plain-language action and real consent controls without identity or argument dumps", () => {
  const html = renderToStaticMarkup(
    <ApprovalCard
      approval={
        {
          id: "private-approval-id",
          toolCallId: "private-tool-id",
          actionHash: "private-hash",
          contentRef: "private-content-ref",
          toolName: "web_fetch",
          arguments: { url: "https://example.com/article" },
          deadline: "2099-01-01T00:00:00Z",
          policyRevision: 0,
          toolsetVersion: "toolset-debug",
        } as any
      }
      policy={{
        revision: 0,
        approvalAll: false,
        toolsetVersion: "toolset-debug",
      }}
      busy={false}
      onDecision={() => {}}
    />,
  );
  expect(html).toContain("https://example.com/article");
  for (const action of ["Approve once", "Approve all", "Deny"])
    expect(html).toContain(action);
  for (const text of [
    "<pre",
    "<details",
    "private-",
    "toolset-debug",
    "Policy revision",
    "Action identity",
  ])
    expect(html).not.toContain(text);
});
