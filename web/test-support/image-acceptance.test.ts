import { expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
  assertTranslationOnly,
  assertTranslationTurn,
} from "./image-acceptance";
const fixture = (name: string) =>
  JSON.parse(readFileSync(`../testdata/web-chat/${name}.json`, "utf8"));
const prompt =
  "Translate the text in the image into English. Reply only with the translation.";
it("retained Qwen narrow-profile contradictory parent is rejected despite completed history and correct tool text (R2 remains open)", () => {
  const f = fixture("qwen-narrow-parent-failure");
  expect(JSON.parse(f.tools[0]).text).toBe("hello world");
  expect(f.history.messages[1].completionState).toBe("completed");
  expect(() =>
    assertTranslationTurn(
      f.history.messages,
      f.history.messages[0].turnId,
      prompt,
    ),
  ).toThrow(/Nonconforming final answer/);
});
it("conforming retained Gemma history and earlier Qwen rendered final pass the same full-answer assertion, not a Qwen requalification", () => {
  const f = fixture("gemma-narrow-parent-conforming");
  expect(() =>
    assertTranslationTurn(
      f.history.messages,
      f.history.messages[0].turnId,
      prompt,
    ),
  ).not.toThrow();
  // Earlier Qwen evidence captured rendered DOM only; don't invent a SQL record.
  const dom = fixture("qwen-narrow-parent-conforming-dom");
  const final = dom.text
    .split("Assistant\nSaved reply\n\n")[1]
    .split("\n\nBounded, lossy context")[0];
  expect(final).toBe("HELLO WORLD");
  expect(() => assertTranslationOnly(final)).not.toThrow();
});
it("cannot qualify from a different turn's good answer or translation plus additional commentary", () => {
  const f = fixture("gemma-narrow-parent-conforming");
  expect(() =>
    assertTranslationTurn(f.history.messages, "other-turn", prompt),
  ).toThrow();
  expect(() =>
    assertTranslationOnly("Hello world. I cannot access images."),
  ).toThrow();
  expect(() => assertTranslationOnly("<think>hello world</think>")).toThrow();
});
