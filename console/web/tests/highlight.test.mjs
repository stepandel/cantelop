import { expect, test } from "bun:test";
import { highlight } from "../components/markdown/highlight.ts";

const text = (lines) => lines.map((line) => line.map((token) => token.text).join("")).join("\n");
const roles = (lines) => lines.flat().filter((token) => token.role).map((token) => `${token.role}:${token.text}`);

test("a shell transcript keeps prompts and answers apart from the syntax", async () => {
  const code = "$ curl -X POST http://127.0.0.1:8787/sessions \\\n    -H 'Content-Type: application/json'\n→ {\"sessionId\":\"ses_...\"}";
  const lines = await highlight(code, "shell", true);
  expect(text(lines)).toBe(code);
  expect(lines[0][0]).toEqual({ text: "$ ", role: "prompt" });
  // Bare arguments are code, not literals: only the prompt carries a role.
  expect(lines[0].filter((token) => token.role).map((token) => token.role)).toEqual(["prompt"]);
  expect(lines[1].filter((token) => token.role)).toEqual([
    { text: "'Content-Type: application/json'", role: "string" },
  ]);
  expect(lines[2]).toEqual([{ text: '→ {"sessionId":"ses_..."}', role: "out" }]);
});

test("typescript gets keywords, strings, comments, and constants, and nothing else", async () => {
  const code = 'const n = 300; // note\nexport default "x";';
  const lines = await highlight(code, "ts", false);
  expect(text(lines)).toBe(code);
  expect(roles(lines)).toEqual([
    "keyword:const",
    "constant:300",
    "comment:// note",
    "keyword:export",
    "keyword:default",
    'string:"x"',
  ]);
});

test("a language without a grammar is plain, with empty lines empty", async () => {
  expect(await highlight("a\n\nb", "text", false)).toEqual([[{ text: "a" }], [], [{ text: "b" }]]);
});

test("every fence language the docs use round-trips through its grammar", async () => {
  for (const [language, code] of [
    ["tsx", "export default <div className=\"x\">{1}</div>;"],
    ["json", '{ "a": true, "b": [1, "two"] }'],
    ["dockerfile", "FROM node:22\nRUN npm ci"],
    ["shell", "$ export A=1\n$ for f in *; do echo \"$f\"; done"],
  ]) {
    const lines = await highlight(code, language, language === "shell");
    expect(text(lines)).toBe(code);
    expect(roles(lines).length).toBeGreaterThan(0);
  }
});
