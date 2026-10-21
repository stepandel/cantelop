"use client";

import { type CSSProperties, type ReactNode } from "react";
import { useCopy } from "@/lib/use-copy";

import type { Line } from "./highlight";
import styles from "./prose.module.css";

/* A code block is a panel with a quiet strip along its top edge. The strip
   says what the block is — the file the fence named, the tab strip when the
   page offers variants, or failing both the kind of thing it is: Terminal,
   TypeScript — and holds the copy control. A block with nothing to name has
   no strip; its copy control sits in the corner instead.

   Snippets are written as plain text with two markers, so one component can
   both render and copy them:

     "$ cantelop dev"   a command — the prompt is decoration, the rest is copied
     "→ listening ..."  what the terminal answered — shown, never copied

   Anything else is a continuation line (a wrapped curl, a JSON body) and
   travels with the command above it. A reader who copies a transcript wants
   the commands, not the prompts and not the output pasted back at their
   shell.

   What is drawn comes in as lines of tokens, each carrying a role or none,
   already worked out on the server by highlight(). The raw code is kept for
   the copy. A file's lines are numbered in the gutter; the numbers are drawn
   by the stylesheet, so a selection never picks them up. */
type Props = {
  code: string;
  lines: Line[];
  shell?: boolean;
  /* The file the fence named, shown in the strip in the typed face. */
  title?: string;
  /* What the block is when no file names it: Terminal, TypeScript, JSON. */
  kind?: string;
  numbered?: boolean;
  /* Set when the snippet is a tab's panel, so the tablist can own it. */
  panel?: { id: string; labelledBy: string };
  /* A control row of the caller's own — <Variants>' tab strip — that stands
     in the strip where the block's name would. */
  head?: ReactNode;
  /* The label a fence carried after its language. Nothing renders it — it is
     how <Variants> knows which tab this block belongs to. */
  variant?: string;
};

export function Snippet({ code, lines, shell = false, title, kind, numbered = false, panel, head }: Props) {
  const { copied, copy: copyToClipboard } = useCopy();
  const copy = () => copyToClipboard(copyText(code, shell));

  const name = title ? (
    <span className={styles.snippetTitle}>{title}</span>
  ) : kind ? (
    <span className={styles.snippetKind}>{kind}</span>
  ) : null;

  const button = (
    <button
      type="button"
      className={styles.copy}
      onClick={copy}
      aria-label={shell ? "Copy commands" : "Copy snippet"}
      title={copied ? "Copied" : "Copy"}
      data-copied={copied || undefined}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
      <span className="visually-hidden" role="status">
        {copied ? "Copied to clipboard" : ""}
      </span>
    </button>
  );

  /* A block with nothing to be called — plain text, an unnamed snippet —
     gets no strip at all; its copy control floats in the top corner. */
  const bare = !head && !name;

  return (
    <div className={`${styles.snippetWrap} ${bare ? styles.bare : ""}`}>
      {bare ? (
        button
      ) : (
        <div className={styles.snippetHead}>
          {head ?? name}
          <span className={styles.snippetEnd}>
            {head && title ? <span className={styles.snippetTitle}>{title}</span> : null}
            {button}
          </span>
        </div>
      )}
      <pre
        className={styles.snippet}
        id={panel?.id}
        role={panel ? "tabpanel" : undefined}
        aria-labelledby={panel?.labelledBy}
        tabIndex={0}
      >
        <code
          className={numbered ? styles.numbered : undefined}
          style={numbered ? ({ "--digits": String(lines.length).length } as CSSProperties) : undefined}
        >
          {lines.map((line, index) => (
            <span key={index} className={styles.line}>
              {line.map((token, at) =>
                token.role ? (
                  <span key={at} className={styles[token.role]}>
                    {token.text}
                  </span>
                ) : (
                  token.text
                ),
              )}
              {"\n"}
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}

function CopyIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <rect x="5.25" y="5.25" width="8.5" height="8.5" rx="1.75" />
      <path d="M10.75 5.25V3.75a1.5 1.5 0 0 0-1.5-1.5h-5.5a1.5 1.5 0 0 0-1.5 1.5v5.5a1.5 1.5 0 0 0 1.5 1.5h1.5" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true">
      <path d="M3 8.5l3.25 3.25L13 5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function copyText(code: string, shell: boolean) {
  if (!shell) {
    return code;
  }
  return code
    .split("\n")
    .filter((line) => !line.startsWith("→"))
    .map((line) => (line.startsWith("$ ") ? line.slice(2) : line))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}
