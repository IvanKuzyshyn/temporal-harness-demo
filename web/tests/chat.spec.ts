import { test, expect, type Page } from "@playwright/test";
import type { Snapshot } from "../lib/contracts";
// Deterministic transport fixtures only. Actual API/Temporal/provider proof lives
// in chat-live.spec.ts and the task report, never inferred from these responses.
const id = "11111111-1111-4111-8111-111111111111";
const file = "22222222-2222-4222-8222-222222222222";
const turn = "33333333-3333-4333-8333-333333333333";
const conversation = {
  id,
  title: "Durable interaction fixture",
  model: "qwen3.5:9b" as const,
  workflowId: "web-chat/" + id,
  lifecycle: "open" as const,
  policyRevision: 0,
  createdAt: "",
  updatedAt: "",
};
function snapshot(): Snapshot {
  return {
    reset: true,
    messages: [],
    before: "",
    turns: [],
    commands: [],
    tools: [],
    attempts: [],
    metadata: {
      conversation,
      activeTurn: { id: turn, status: "awaiting_approval", updatedAt: "" },
      pendingApproval: {
        id: "approval-one",
        toolCallId: "tool",
        toolName: "file_upload",
        arguments: {
          attachmentId: file,
          instruction: "Translate visible text into English.",
        },
        contentRef: "checksum",
        actionHash: "a".repeat(64),
        deadline: "2099-01-01T00:00:00Z",
        policyRevision: 0,
        toolsetVersion: "web_fetch@1,file_upload@1",
      },
      policy: {
        approvalAll: false,
        revision: 0,
        toolsetVersion: "web_fetch@1,file_upload@1",
      },
      freshness: {
        source: "postgres_projection",
        authoritative: false,
        observedAt: "",
        workflowConfirmedAt: null,
      },
      dispatcherConnected: true,
    },
  };
}
async function fixture(page: Page) {
  let s = snapshot();
  const posts: { path: string; body: Record<string, unknown> }[] = [];
  let uploads = 0;
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j0WQAAAAASUVORK5CYII=",
    "base64",
  );
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    if (path.endsWith("/events"))
      return route.fulfill({
        contentType: "text/event-stream",
        body: `id: 1\ndata: ${JSON.stringify({ id: "1", conversationId: id, turnId: null, stepId: null, attempt: null, type: "snapshot", data: s })}\n\n`,
      });
    if (path.endsWith("/attachments") && req.method() === "POST") {
      uploads++;
      return route.fulfill({
        status: 201,
        json: {
          attachment: {
            id: file,
            conversationId: id,
            name: "text.png",
            mediaType: "image/png",
            size: png.length,
            checksum: "checksum",
            status: "ready",
          },
        },
      });
    }
    if (path.endsWith("/attachments/" + file))
      return route.fulfill({
        contentType: "image/png",
        headers: {
          "x-content-type-options": "nosniff",
          "content-disposition": "attachment",
        },
        body: png,
      });
    if (req.method() === "POST") {
      const body = req.postDataJSON();
      posts.push({ path, body });
      const r = {
        commandId: `command-${posts.length}`,
        requestId: body.requestId,
        turnId: turn,
        payloadDigest: "digest",
        dispatchState: "pending",
        resultState: "pending",
        result: null,
      } as const;
      s.commands.push(r);
      return route.fulfill({ status: 202, json: r });
    }
    if (path.endsWith("/conversations"))
      return route.fulfill({
        json: { conversations: [conversation], cursor: "" },
      });
    if (path.includes("/commands/"))
      return route.fulfill({
        json: s.commands.find((c) => path.endsWith(c.commandId)),
      });
    return route.fulfill({ json: s.metadata });
  });
  await page.goto(`/chat/${id}`);
  await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  return {
    posts,
    get uploads() {
      return uploads;
    },
    get state() {
      return s;
    },
    async refresh(next: Snapshot) {
      s = next;
      await page
        .getByRole("button", { name: "Reconnect live updates" })
        .click();
    },
  };
}
test("approvalOnceAndAll", async ({ page }) => {
  const f = await fixture(page);
  await page.getByRole("button", { name: "Approve once", exact: true }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].body).toMatchObject({
    actionHash: "a".repeat(64),
    decision: "once",
  });
  await expect(
    page.getByText(/Decision · saved · awaiting execution/),
  ).toBeVisible();
  f.state.commands[0] = {
    ...f.state.commands[0],
    resultState: "rejected",
    result: { state: "rejected", code: "invalid_approval" },
  };
  await f.refresh(f.state);
  await expect(
    page.getByText(/Decision · rejected: invalid_approval/),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Approve all for this conversation" })
    .click();
  await expect.poll(() => f.posts.length).toBe(2);
  expect(f.posts[1].body.decision).toBe("all");
  f.state.commands[1] = { ...f.state.commands[1], resultState: "accepted" };
  f.state.metadata.pendingApproval = null;
  f.state.metadata.policy.approvalAll = true;
  await f.refresh(f.state);
  await expect(
    page.getByRole("button", { name: "Revoke approval" }),
  ).toBeVisible();
});
test("approvalRevokeAfterReload", async ({ page }) => {
  const f = await fixture(page);
  f.state.metadata.policy.approvalAll = true;
  await page.reload();
  await page.getByRole("button", { name: "Revoke approval" }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].path).toMatch(/approval-policy\/revoke$/);
  await expect(
    page.getByRole("button", { name: "Revoke approval" }),
  ).toBeVisible();
  f.state.commands[0] = { ...f.state.commands[0], resultState: "accepted" };
  f.state.metadata.policy.approvalAll = false;
  await f.refresh(f.state);
  await expect(
    page.getByRole("button", { name: "Revoke approval" }),
  ).toHaveCount(0);
});
test("replaceDoesNotCancelNewerTurn", async ({ page }) => {
  const f = await fixture(page);
  await page.getByRole("textbox").fill("Replacement instruction");
  await page.getByRole("button", { name: "Replace current turn" }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].body.expectedActiveTurnId).toBe(turn);
  f.state.metadata.activeTurn = {
    id: "new-turn",
    status: "running",
    updatedAt: "",
  };
  f.state.commands[0] = {
    ...f.state.commands[0],
    resultState: "rejected",
    result: { code: "stale_target" },
  };
  await f.refresh(f.state);
  await expect(
    page.getByText("An action could not be completed. Refresh and try again."),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Stop current turn" }),
  ).toBeEnabled();
  expect(f.posts).toHaveLength(1);
});
test("uploadAndTranslateImage", async ({ page }) => {
  const f = await fixture(page);
  await page.getByLabel("Attach files").setInputFiles({
    name: "text.png",
    mimeType: "image/png",
    buffer: Buffer.from("test fixture; server validation separately tested"),
  });
  await page.getByRole("button", { name: "Preview raster" }).click();
  const image = page.getByRole("img", { name: "Preview of text.png" });
  await expect(image).toBeVisible();
  await expect
    .poll(() => image.evaluate((el) => (el as HTMLImageElement).naturalWidth))
    .toBe(1);
  await page.getByRole("textbox").fill("Translate visible text into English.");
  await page.getByRole("button", { name: "Send message" }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].body.attachmentIds).toEqual([file]);
  // This is transport proof, not a generated translation.
  expect(f.posts[0].path).toMatch(/messages$/);
});
test("reopenAttachmentFollowUp", async ({ page }) => {
  const f = await fixture(page);
  f.state.messages = [
    {
      entryId: "old-image",
      conversationId: id,
      turnId: turn,
      role: "user",
      text: "Original image instruction",
      attachmentIds: [file],
      logicalStep: 0,
      resultKind: "input",
      completionState: "completed",
      context: null,
      sequence: 1,
      createdAt: "",
    },
  ];
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await page.getByRole("button", { name: "Attach again " + file }).click();
  await page.getByRole("textbox").fill("Describe that image.");
  await page.getByRole("button", { name: "Send message" }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].body.attachmentIds).toEqual([file]);
  await expect(page.locator("img,iframe,object")).toHaveCount(0);
});
test("queuedMessageVisible", async ({ page }) => {
  const f = await fixture(page);
  await page.getByRole("textbox").fill("Next question");
  await page.getByRole("textbox").press("Enter");
  await expect.poll(() => f.posts.length).toBe(1);
  f.state.turns = [{ id: "follow-up", status: "queued", reason: "" }];
  await f.refresh(f.state);
  await expect(page.getByText("Follow-up queued")).toBeVisible();
  expect(f.posts[0].path).toMatch(/messages$/);
});
test("switchChatRetainsDraft", async ({ page }) => {
  await fixture(page);
  await page.getByRole("textbox").fill("Unsent draft");
  await page.getByRole("link", { name: "New chat", exact: true }).click();
  await page.getByRole("button", { name: "Toggle sidebar" }).click();
  await page.getByRole("link", { name: /Durable interaction fixture/ }).click();
  await expect(page.getByRole("textbox")).toHaveValue("Unsent draft");
});
test("pendingApprovalAlwaysVisible and keyboard/narrow layout", async ({
  page,
}) => {
  await page.setViewportSize({ width: 375, height: 812 });
  const f = await fixture(page);
  f.state.commands = Array.from({ length: 4 }, (_, i) => ({
    commandId: `old-rejection-${i}`,
    turnId: null,
    payloadDigest: "digest",
    dispatchState: "dispatched",
    resultState: "rejected",
    result: { code: "stale_target" },
  }));
  await f.refresh(f.state);
  await expect(
    page.getByText("An action could not be completed. Refresh and try again."),
  ).toHaveCount(1);
  await expect(page.locator("details")).toHaveCount(0);
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  expect(
    await page
      .getByRole("button", { name: "Approve once", exact: true })
      .evaluate((el) => !!el.closest("details")),
  ).toBe(false);
  for (const name of [
    "Approve once",
    "Approve all for this conversation",
    "Deny",
  ]) {
    expect(
      await page.getByRole("button", { name, exact: true }).evaluate((el) => {
        const button = el.getBoundingClientRect(),
          panel = el.closest(".approval-card")!.getBoundingClientRect();
        return button.top >= panel.top && button.bottom <= panel.bottom;
      }),
    ).toBe(true);
  }
  await page.getByRole("button", { name: "Approve once", exact: true }).focus();
  await expect(
    page.getByRole("button", { name: "Approve once", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(
    page.getByRole("button", { name: "Approve all for this conversation" }),
  ).toBeFocused();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("mobile sidebar Escape restores focus to the visible opener", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await fixture(page);
  await page.getByRole("button", { name: "Toggle sidebar" }).click();
  await expect(
    page.getByRole("button", { name: "Close sidebar", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "Toggle sidebar" }),
  ).toBeFocused();
});

test("single attachment: multi-drop, second selection and reload cannot discard or upload extras", async ({
  page,
}) => {
  const f = await fixture(page);
  expect(
    await page.getByLabel("Attach files").getAttribute("multiple"),
  ).toBeNull();
  await page.evaluate(() => {
    const transfer = new DataTransfer();
    transfer.items.add(new File(["a"], "a.txt", { type: "text/plain" }));
    transfer.items.add(new File(["b"], "b.txt", { type: "text/plain" }));
    document
      .querySelector(".attachment-picker")!
      .dispatchEvent(
        new DragEvent("drop", { bubbles: true, dataTransfer: transfer }),
      );
  });
  await expect(
    page.getByRole("region", { name: "Attachments" }).getByRole("alert"),
  ).toContainText("one attachment per message");
  expect(f.uploads).toBe(0);
  await page
    .getByLabel("Attach files")
    .setInputFiles({
      name: "text.png",
      mimeType: "image/png",
      buffer: Buffer.from("fixture"),
    });
  await expect(
    page.getByRole("button", { name: "Remove text.png" }),
  ).toBeVisible();
  await page
    .getByLabel("Attach files")
    .setInputFiles({
      name: "second.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("fixture"),
    });
  await expect(
    page.getByRole("region", { name: "Attachments" }).getByRole("alert"),
  ).toContainText("one attachment per message");
  expect(f.uploads).toBe(1);
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Remove text.png" }),
  ).toBeVisible();
  await page.getByRole("textbox").fill("Keep this attachment");
  await page.getByRole("button", { name: "Send message" }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].body.attachmentIds).toEqual([file]);
});

test("legacy multi-file pending draft requires explicit edit and removal before replacement", async ({
  page,
}) => {
  const f = await fixture(page);
  await page.evaluate(
    ({ id, file }) => {
      localStorage.setItem(
        "chat.attachments." + id,
        JSON.stringify([
          { id: file, name: "one.txt" },
          { id: "second", name: "two.txt" },
        ]),
      );
      localStorage.setItem(
        "chat.draft." + id,
        JSON.stringify("Retained draft"),
      );
      localStorage.setItem(
        "chat.pending." + id,
        JSON.stringify({
          requestId: "old",
          text: "Retained draft",
          attachmentIds: [file, "second"],
        }),
      );
    },
    { id, file },
  );
  await page.reload();
  await expect(page.getByText(/one attachment per message/)).toBeVisible();
  await page.getByRole("button", { name: "Retry saved submission" }).click();
  expect(f.posts).toHaveLength(0);
  await page.getByRole("button", { name: "Edit saved submission" }).click();
  await page.getByRole("button", { name: "Remove two.txt" }).click();
  await expect(page.getByRole("textbox")).toHaveValue("Retained draft");
  await page.getByRole("button", { name: "Replace current turn" }).click();
  await expect.poll(() => f.posts.length).toBe(1);
  expect(f.posts[0].body.attachmentIds).toEqual([file]);
  expect(f.posts[0].body.expectedActiveTurnId).toBe(turn);
});
