import { createHighlighterCore, type HighlighterCore, type ThemeRegistrationRaw } from "shiki/core";
import { createOnigurumaEngine } from "shiki/engine/oniguruma";

/* Syntax roles for a code block. Server-side: the MDX <pre> override calls
   highlight() while the page renders, and the client Snippet only ever sees
   the tokens, so the grammars never ship to the browser.

   Four syntax roles is deliberate. On paper, a full editor theme reads as
   noise; what helps a reader parse a block is telling a keyword from a name,
   a literal from code, and a comment from either. The two transcript roles
   are the shell treatment the Snippet always had: the prompt is decoration,
   the answer is what proves the step worked. */
export type Role = "prompt" | "out" | "keyword" | "string" | "comment" | "constant";
export type Token = { text: string; role?: Role };
export type Line = Token[];

/* The theme paints nothing. Each rule assigns a sentinel color that maps to
   a role, so the palette lives in prose.module.css with the rest of the
   snippet's styling instead of being baked into a theme file. */
const ROLES: Record<string, Role> = {
  "#000001": "keyword",
  "#000002": "string",
  "#000003": "comment",
  "#000004": "constant",
};

const THEME: ThemeRegistrationRaw = {
  name: "roles",
  type: "light",
  settings: [
    { settings: { foreground: "#000000" } },
    {
      scope: [
        "keyword.control",
        "keyword.operator.new",
        "keyword.operator.expression",
        "keyword.other.special-method",
        "storage.type",
        "storage.modifier",
      ],
      settings: { foreground: "#000001" },
    },
    // Quoted literals only. The bash grammar scopes every bare argument as
    // string.unquoted, and painting `-X POST` as a literal says nothing.
    {
      scope: [
        "string.quoted",
        "string.template",
        "string.interpolated",
        "string.regexp",
        "punctuation.definition.string",
      ],
      settings: { foreground: "#000002" },
    },
    { scope: ["comment", "punctuation.definition.comment"], settings: { foreground: "#000003" } },
    { scope: ["constant.numeric", "constant.language"], settings: { foreground: "#000004" } },
  ],
};

/* Fence tags the docs use, resolved to the grammar that reads them. A tag
   not listed here is plain text. */
const GRAMMARS: Record<string, string> = {
  bash: "bash",
  sh: "bash",
  shell: "bash",
  console: "bash",
  ts: "typescript",
  typescript: "typescript",
  tsx: "tsx",
  json: "json",
  dockerfile: "dockerfile",
};

let core: Promise<HighlighterCore> | undefined;

function highlighter() {
  core ??= createHighlighterCore({
    themes: [THEME],
    langs: [
      import("@shikijs/langs/bash"),
      import("@shikijs/langs/typescript"),
      import("@shikijs/langs/tsx"),
      import("@shikijs/langs/json"),
      import("@shikijs/langs/dockerfile"),
    ],
    // Oniguruma, not shiki's JavaScript regex engine: the latter reads the
    // TypeScript grammar wrong and hands back whole lines as one token. The
    // binary comes inlined, so nothing depends on a .wasm file surviving the
    // server bundle.
    engine: createOnigurumaEngine(import("shiki/wasm")),
  });
  return core;
}

/* Lines of tokens for a block, one line per line of `code`, whose texts
   concatenate back to the code exactly. A shell transcript is read line by
   line: "$ " starts a command, "→" starts the terminal's answer, and any
   other line continues the command above it. */
export async function highlight(code: string, language: string, shell: boolean): Promise<Line[]> {
  if (shell) {
    return Promise.all(code.split("\n").map(shellLine));
  }
  return tokenize(code, language);
}

async function shellLine(line: string): Promise<Line> {
  if (line.startsWith("→")) {
    return [{ text: line, role: "out" }];
  }
  if (line.startsWith("$ ")) {
    const [command] = await tokenize(line.slice(2), "bash");
    return [{ text: "$ ", role: "prompt" }, ...command];
  }
  const [continued] = await tokenize(line, "bash");
  return continued;
}

async function tokenize(code: string, language: string): Promise<Line[]> {
  const grammar = GRAMMARS[language];
  if (!grammar) {
    return plain(code);
  }
  const shiki = await highlighter();
  return shiki
    .codeToTokensBase(code, { lang: grammar, theme: THEME.name })
    .map((line) => merge(line.map((token) => ({ text: token.content, role: ROLES[token.color?.toLowerCase() ?? ""] }))));
}

function plain(code: string): Line[] {
  return code.split("\n").map((line) => (line ? [{ text: line }] : []));
}

/* The grammar splits finely — a string's quotes are their own tokens — and
   every token becomes an element. Neighbours sharing a role fold together. */
function merge(line: Token[]): Line {
  const out: Line = [];
  for (const token of line) {
    if (!token.text) continue;
    const last = out[out.length - 1];
    if (last && last.role === token.role) {
      last.text += token.text;
    } else {
      out.push(token.role ? { text: token.text, role: token.role } : { text: token.text });
    }
  }
  return out;
}
