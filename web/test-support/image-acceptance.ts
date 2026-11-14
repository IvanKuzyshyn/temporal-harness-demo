import assert from "node:assert/strict";

// Test-only acceptance for scripts/testdata/bonjour.png (BONJOUR LE MONDE).
// This checks the entire final answer, never strips reasoning/refusals or uses
// tool success as parent success. It is not production phrase-based behavior.
export function assertTranslationOnly(text: string) {
  assert.match(
    text.trim(),
    /^hello world[.!]?$/i,
    "Nonconforming final answer: expected only the English translation 'Hello world', not reasoning, a disclaimer or a denial of image access",
  );
}
type Record = {
  role: string;
  turnId: string;
  resultKind: string;
  completionState: string;
  text: string;
};
export function assertTranslationTurn(
  messages: Record[],
  turnId: string,
  prompt: string,
) {
  const user = messages.find(
    (m) =>
      m.turnId === turnId && m.role === "user" && m.resultKind === "prompt",
  );
  assert.equal(
    user?.text,
    prompt,
    "the submitted instruction must remain unchanged",
  );
  const finals = messages.filter(
    (m) =>
      m.turnId === turnId &&
      m.role === "assistant" &&
      m.resultKind === "final" &&
      m.completionState === "completed",
  );
  assert.equal(
    finals.length,
    1,
    "one saved parent final required for this exact turn",
  );
  assertTranslationOnly(finals[0].text);
}
