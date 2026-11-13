import { test, expect, type Page } from "@playwright/test";
import { resolve } from "node:path";
import { mkdir, writeFile } from "node:fs/promises";
// Opt-in owned runtime from TestStreamBrowserRuntime. No fake API/model/SSE.
// A held control request below is a browser transport fault, then continued to
// the actual API; separate competing commands establish the stale-target race.
test.skip(
  !process.env.WEBCHAT_TEST_BROWSER_URL,
  "requires owned real Postgres/Temporal/API/worker runtime",
);
const output = process.env.WEBCHAT_BROWSER_EVIDENCE;
async function api(page: Page, path: string, body?: unknown) {
  return page.evaluate(
    async ({ path, body }) => {
      const r = await fetch("/api" + path, {
        method: body === undefined ? "GET" : "POST",
        headers:
          body === undefined
            ? undefined
            : { "content-type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (!r.ok) throw new Error(`API ${r.status}: ${await r.text()}`);
      return r.json();
    },
    { path, body },
  );
}
async function evidence(name: string, data: unknown) {
  if (output) {
    await mkdir(output, { recursive: true });
    await writeFile(
      resolve(output, name + ".json"),
      JSON.stringify(data, null, 2),
    );
  }
}
async function capture(page: Page, name: string) {
  if (output) await page.screenshot({ path: resolve(output, name + ".png") });
}
async function completed(page: Page, id: string, count: number) {
  await expect
    .poll(
      async () =>
        (await api(page, `/conversations/${id}/messages`)).messages.filter(
          (m: { role: string }) => m.role === "assistant",
        ).length,
      { timeout: 180000 },
    )
    .toBeGreaterThanOrEqual(count);
  await expect
    .poll(async () => (await api(page, `/conversations/${id}`)).activeTurn, {
      timeout: 30000,
    })
    .toBeNull();
}
async function send(page: Page, text: string) {
  await page.getByRole("textbox", { name: "Message" }).fill(text);
  await page.getByRole("button", { name: "Send message" }).click();
}
async function create(page: Page, model: string) {
  await page.goto("/");
  await page.getByLabel("Local model").selectOption(model);
  await page.getByRole("button", { name: "Start conversation" }).click();
  await expect(page).toHaveURL(/\/chat\//);
  await expect(page.getByText(model, { exact: false }).last()).toBeVisible();
  return new URL(page.url()).pathname.split("/").at(-1)!;
}

test("real uploadAndTranslateImage / reopenAttachmentFollowUp / approvalOnceAndAll / approvalRevokeAfterReload", async ({
  page,
}) => {
  test.setTimeout(480000);
  const consoleErrors: string[] = [],
    network: { url: string; status: number }[] = [];
  page.on("pageerror", (e) => consoleErrors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") consoleErrors.push(m.text());
  });
  page.on("response", (r) =>
    network.push({ url: r.url(), status: r.status() }),
  );
  await page.setViewportSize({ width: 1440, height: 1000 });
  const id = await create(page, "gemma4:e4b");
  await page
    .getByLabel("Attach files")
    .setInputFiles(resolve("../scripts/testdata/bonjour.png"));
  await expect(page.getByText("bonjour.png", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Preview raster" }).click();
  await expect(page.getByRole("img")).toBeVisible();
  await expect
    .poll(() =>
      page
        .getByRole("img")
        .evaluate((e) => (e as HTMLImageElement).naturalWidth),
    )
    .toBe(640);
  await capture(page, "live-desktop-ready-raster");
  await send(
    page,
    "Translate the text in the image into English. Reply only with the translation.",
  );
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  const first = await api(page, `/conversations/${id}`);
  expect(first.pendingApproval.toolName).toBe("file_upload");
  await capture(page, "live-desktop-approval");
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Approve once", exact: true }).click();
  await completed(page, id, 1);
  await capture(page, "live-desktop-translation");
  const firstHistory = await api(page, `/conversations/${id}/messages`);
  const attachmentId = firstHistory.messages[0].attachmentIds[0];
  await evidence("live-first-image", {
    id,
    first,
    history: firstHistory,
    rendered: await page.locator(".tool-result").allTextContents(),
  });
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await page
    .getByRole("button", { name: `Attach again ${attachmentId}` })
    .first()
    .click();
  await send(
    page,
    "Translate the same image into English again. Reply only with the translation.",
  );
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  const followUp = await api(page, `/conversations/${id}`);
  expect(followUp.pendingApproval.id).not.toBe(first.pendingApproval.id);
  expect(followUp.pendingApproval.actionHash).not.toBe(
    first.pendingApproval.actionHash,
  );
  await page.reload();
  await page
    .getByRole("button", { name: "Approve all for this conversation" })
    .click();
  await completed(page, id, 2);
  await page.reload();
  await expect(page.getByText("Conversation approval enabled")).toBeVisible();
  await page.getByRole("button", { name: "Revoke approval" }).click();
  await expect(page.getByText("Conversation approval enabled")).toHaveCount(0);
  await page
    .getByRole("button", { name: `Attach again ${attachmentId}` })
    .first()
    .click();
  await send(page, "Describe the visible text without translating.");
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  await page.reload();
  await page.getByRole("button", { name: "Deny", exact: true }).click();
  await completed(page, id, 3);
  await evidence("live-image-follow-up-denial-revoke", {
    id,
    followUp,
    metadata: await api(page, `/conversations/${id}`),
    history: await api(page, `/conversations/${id}/messages`),
    tools: await page.locator(".tool-result").allTextContents(),
  });
  // Narrow layout + keyboard and switching preserve the actual conversation draft.
  await page.setViewportSize({ width: 375, height: 812 });
  await page.getByRole("textbox").fill("Unsent image follow-up");
  await page.getByRole("textbox").press("Tab");
  await expect(page.getByLabel("Attach files")).toBeFocused();
  await page.getByRole("button", { name: "Toggle sidebar" }).click();
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "Toggle sidebar" }),
  ).toBeFocused();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await capture(page, "live-mobile-saved");
  await page.goto("/");
  await page.goto(`/chat/${id}`);
  await expect(page.getByRole("textbox")).toHaveValue("Unsent image follow-up");
  await evidence("live-console-network", { consoleErrors, network });
  expect(consoleErrors).toEqual([]);
  expect(
    network.every((n) => new URL(n.url).origin === new URL(page.url()).origin),
  ).toBe(true);
});

test("real queuedMessageVisible / stale stop and replaceDoesNotCancelNewerTurn", async ({
  page,
}) => {
  test.setTimeout(240000);
  const id = await create(page, "gemma4:e4b");
  const base = `/conversations/${id}`;
  await page
    .getByLabel("Attach files")
    .setInputFiles({
      name: "notes.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("The project color is green."),
    });
  await expect(page.getByText("notes.txt", { exact: true })).toBeVisible();
  await send(page, "Summarize this attachment.");
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  const old = await api(page, base);
  await send(page, "Queued normal follow-up: say hello.");
  await expect(page.getByText("1 queued message")).toBeVisible();
  await page.getByRole("textbox").fill("Replacement: say replacement first.");
  await page.getByRole("button", { name: "Replace current turn" }).click();
  await completed(page, id, 2);
  const history = await api(page, base + "/messages");
  const assistants = history.messages.filter(
    (m: { role: string }) => m.role === "assistant",
  );
  const replacement = history.messages.find(
    (m: { text: string }) => m.text === "Replacement: say replacement first.",
  );
  const queued = history.messages.find(
    (m: { text: string }) => m.text === "Queued normal follow-up: say hello.",
  );
  expect(assistants.map((m: { turnId: string }) => m.turnId)).toEqual([
    replacement.turnId,
    queued.turnId,
  ]);
  // Park a new real turn at approval. Hold UI Stop until a competing replacement
  // cancels it and a newer real turn is active, then send the original body.
  const attachmentId = history.messages[0].attachmentIds[0];
  await page
    .getByRole("button", { name: `Attach again ${attachmentId}` })
    .first()
    .click();
  await send(page, "Analyze this again.");
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  const target = await api(page, base);
  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  let held: unknown;
  await page.route(`**/api${base}/stop`, async (route) => {
    held = route.request().postDataJSON();
    await gate;
    await route.continue();
  });
  await page.getByRole("button", { name: "Stop current turn" }).click();
  await expect.poll(() => held).toBeTruthy();
  const newer = await api(page, base + "/replace", {
    requestId: crypto.randomUUID(),
    expectedActiveTurnId: target.activeTurn.id,
    text: "Analyze replacement attachment.",
    attachmentIds: [attachmentId],
  });
  await expect
    .poll(async () => (await api(page, base)).activeTurn?.id)
    .toBe(newer.turnId);
  release();
  await expect(page.getByText(/Stop · rejected: stale_target/)).toBeVisible();
  expect((held as { expectedActiveTurnId: string }).expectedActiveTurnId).toBe(
    target.activeTurn.id,
  );
  expect((await api(page, base)).activeTurn.id).toBe(newer.turnId);
  await page.unroute(`**/api${base}/stop`);
  // The same real race for replacement; its draft is immutable once dispatched.
  let releaseReplace!: () => void, heldReplace: unknown;
  const gateReplace = new Promise<void>((r) => {
    releaseReplace = r;
  });
  await page.route(`**/api${base}/replace`, async (route) => {
    heldReplace = route.request().postDataJSON();
    await gateReplace;
    await route.continue();
  });
  await page.getByRole("textbox").fill("Stale correcting message");
  await page.getByRole("button", { name: "Replace current turn" }).click();
  await expect.poll(() => heldReplace).toBeTruthy();
  await api(page, base + "/stop", {
    requestId: crypto.randomUUID(),
    expectedActiveTurnId: newer.turnId,
  });
  await expect.poll(async () => (await api(page, base)).activeTurn).toBeNull();
  const newest = await api(page, base + "/messages", {
    requestId: crypto.randomUUID(),
    text: "A newer attachment action",
    attachmentIds: [attachmentId],
  });
  await expect
    .poll(async () => (await api(page, base)).activeTurn?.id)
    .toBe(newest.turnId);
  releaseReplace();
  await expect
    .poll(async () => {
      const rows = (await api(page, base + "/messages")).messages;
      return rows.find(
        (m: { text: string }) => m.text === "Stale correcting message",
      )?.turnStatus;
    })
    .toBe("failed");
  await expect(
    page.getByText(/Command rejected: stale_target/).first(),
  ).toBeVisible();
  expect((await api(page, base)).activeTurn.id).toBe(newest.turnId);
  await page.setViewportSize({ width: 375, height: 812 });
  await capture(page, "live-mobile-stale-controls");
  await evidence("live-real-controls", {
    id,
    old,
    history,
    target,
    held,
    newer,
    heldReplace,
    newest,
    metadata: await api(page, base),
    finalHistory: await api(page, base + "/messages"),
  });
  await api(page, base + "/stop", {
    requestId: crypto.randomUUID(),
    expectedActiveTurnId: newest.turnId,
  });
  await page.unroute(`**/api${base}/replace`);
});
