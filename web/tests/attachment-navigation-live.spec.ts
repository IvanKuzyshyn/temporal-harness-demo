import { test, expect } from "@playwright/test";

// No retries or approved model actions: one physical provider call is not the
// same as an image workflow. This suite deliberately invokes neither.
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
// Model-free real API regression. Hold delivery of actual responses, never
// fabricate status/body. Invalid PNG bytes exercise the real signature parser.
test.skip(
  !process.env.WEBCHAT_TEST_BROWSER_URL,
  "requires isolated real API runtime",
);
for (const failure of [false, true]) {
  test(`real multi-file navigation with remount during batch (late failure=${failure})`, async ({
    page,
  }) => {
    await page.goto("/");
    await page.getByRole("button", { name: "Start conversation" }).click();
    await expect(page).toHaveURL(/\/chat\//);
    const path = new URL(page.url()).pathname;
    const id = path.split("/").at(-1)!;
    const responses: { status: number; body: unknown }[] = [];
    const gates: (() => void)[] = [];
    let calls = 0;
    await page.route(
      `**/api/conversations/${id}/attachments`,
      async (route) => {
        const n = calls++;
        const response = await route.fetch();
        responses.push({
          status: response.status(),
          body: await response.json(),
        });
        if (n === 0 || n === 2)
          await new Promise<void>((r) => {
            gates[n] = r;
          });
        await route.fulfill({ response });
      },
    );
    try {
      await page.getByRole("textbox").fill("Unsent batch draft");
      await page.getByLabel("Attach files").setInputFiles([
        {
          name: "one.txt",
          mimeType: "text/plain",
          buffer: Buffer.from("first file"),
        },
        {
          name: failure ? "bad.png" : "two.txt",
          mimeType: failure ? "image/png" : "text/plain",
          buffer: Buffer.from(
            failure ? "<svg onload=alert(1)>not a png</svg>" : "second file",
          ),
        },
        {
          name: "three.txt",
          mimeType: "text/plain",
          buffer: Buffer.from("third file"),
        },
      ]);
      await expect.poll(() => !!gates[0]).toBe(true);
      await page.getByRole("link", { name: "New chat", exact: true }).click();
      gates[0]();
      await expect.poll(() => calls).toBe(3);
      await expect.poll(() => !!gates[2]).toBe(true);
      await page.getByRole("button", { name: "Start conversation" }).click();
      await expect(page).toHaveURL(/\/chat\//);
      expect(new URL(page.url()).pathname).not.toBe(path);
      await expect(page.getByRole("textbox")).toHaveValue("");
      await expect(
        page.getByRole("region", { name: "Attachments" }),
      ).not.toContainText("one.txt");
      await expect(
        page.getByRole("region", { name: "Attachments" }),
      ).not.toContainText("three.txt");
      // SPA navigation retains the original batch; a fresh mounted picker must
      // observe its lock/progress instead of presenting an idle partial result.
      await page.locator(`a[href="${path}"]`).click();
      await expect(
        page.getByRole("region", { name: "Attachments" }),
      ).toHaveAttribute("aria-busy", "true");
      await expect(page.getByLabel("Attach files")).toBeDisabled();
      await expect(
        page.getByRole("button", { name: "Send message" }),
      ).toBeDisabled();
      if (failure)
        await expect(
          page.getByRole("region", { name: "Attachments" }).getByRole("alert"),
        ).toContainText("bad.png");
      gates[2]();
      await expect(
        page.getByRole("region", { name: "Attachments" }),
      ).toHaveAttribute("aria-busy", "false");
      await expect(page.getByRole("textbox")).toHaveValue("Unsent batch draft");
      await expect(page.getByText("one.txt", { exact: true })).toBeVisible();
      await expect(page.getByText("three.txt", { exact: true })).toBeVisible();
      if (failure)
        await expect(
          page.getByRole("region", { name: "Attachments" }).getByRole("alert"),
        ).toContainText("Renaming a file does not convert it");
      else
        await expect(page.getByText("two.txt", { exact: true })).toBeVisible();
      await page.reload();
      if (failure)
        await expect(
          page.getByRole("region", { name: "Attachments" }).getByRole("alert"),
        ).toContainText("bad.png");
      const selected = await page.evaluate(
        (id) => JSON.parse(localStorage.getItem("chat.attachments." + id)!),
        id,
      );
      expect(selected).toHaveLength(failure ? 2 : 3);
      // Every selected identity came from this conversation's real ready reply.
      for (const a of selected)
        expect(
          responses.some((r) => {
            const b = r.body as {
              attachment?: {
                id: string;
                conversationId: string;
                status: string;
              };
            };
            return (
              !!b.attachment &&
              b.attachment.id === a.id &&
              b.attachment.conversationId === id &&
              b.attachment.status === "ready"
            );
          }),
        ).toBe(true);
    } finally {
      const out = process.env.WEBCHAT_BROWSER_EVIDENCE;
      if (out) {
        await mkdir(out, { recursive: true });
        await writeFile(
          resolve(out, `batch-${failure}.json`),
          JSON.stringify(
            {
              id,
              calls,
              responses,
              browser: await page.evaluate(
                (id) => ({
                  selected: localStorage.getItem("chat.attachments." + id),
                  batch: localStorage.getItem("chat.upload-batch." + id),
                  busy: document
                    .querySelector(".attachment-picker")
                    ?.getAttribute("aria-busy"),
                  text: document.body.innerText,
                }),
                id,
              ),
            },
            null,
            2,
          ),
        );
        await page.screenshot({ path: resolve(out, `batch-${failure}.png`) });
      }
      gates.forEach((release) => release());
      await page.unrouteAll({ behavior: "wait" });
    }
  });
}

test("real Stop at file approval shows neutral cancellation without inference", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Start conversation" }).click();
  await expect(page).toHaveURL(/\/chat\//);
  const id = new URL(page.url()).pathname.split("/").at(-1)!;
  await page.getByLabel("Attach files").setInputFiles({
    name: "cancel.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Never analyze: Stop before approval."),
  });
  await expect(
    page.getByRole("region", { name: "Attachments" }),
  ).toHaveAttribute("aria-busy", "false");
  await page.getByRole("textbox").fill("Summarize the attached text.");
  await page.getByRole("button", { name: "Send message" }).click();
  await expect(
    page.getByRole("region", { name: "Approval needed" }),
  ).toBeVisible();
  // Attachment preflight parks before any provider invocation. Never approve.
  await page
    .getByRole("button", { name: "Stop current turn", exact: true })
    .click();
  await expect(
    page.getByText(
      "Analysis cancelled. No new result was produced; the saved attachment remains available.",
    ),
  ).toBeVisible();
  await expect(page.getByText(/If this was an image/)).toHaveCount(0);
  await expect(
    page.getByText("Cancelled · stopped", { exact: true }),
  ).toBeVisible();
  const response = await page.request.get(`/api/conversations/${id}/messages`);
  expect(response.ok()).toBe(true);
  const history = await response.json();
  // Preflight has no provider candidate: cancellation does not invent an
  // assistant record. Only the original prompt/attachment remains canonical.
  expect(history.messages).toHaveLength(1);
  expect(history.messages[0].role).toBe("user");
  expect(history.messages[0].attachmentIds).toHaveLength(1);
  const metadata = await (
    await page.request.get(`/api/conversations/${id}`)
  ).json();
  expect(metadata.activeTurn).toBeNull();
  const out = process.env.WEBCHAT_BROWSER_EVIDENCE;
  if (out) {
    await mkdir(out, { recursive: true });
    await writeFile(
      resolve(out, "cancel-no-inference.json"),
      JSON.stringify(
        { id, history, metadata, text: await page.locator("body").innerText() },
        null,
        2,
      ),
    );
    await page.screenshot({ path: resolve(out, "cancel-no-inference.png") });
  }
});
