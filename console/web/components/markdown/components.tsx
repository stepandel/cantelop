import type { MDXComponents } from "mdx/types";
import { isValidElement, type ReactElement, type ReactNode } from "react";

import { kindOf, parseFence } from "./fence";
import { highlight } from "./highlight";
import { Snippet } from "./snippet";
import styles from "./prose.module.css";

/* The <code> inside a fence's <pre>: its language as a class, and its meta —
   the words after the language — as a prop, put there by
   rehype-fence-meta.mjs. */
type CodeElement = ReactElement<{ className?: string; children?: string; meta?: string }>;

/* The text of a heading, with any inline markup (a `code` span, emphasis)
   flattened out, so the id is the words alone. */
function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children);
  return "";
}

/* "runtime configuration" → "runtime-configuration": the fragment a link to
   the section uses, and what the docs already link to by hand. */
export function headingId(children: ReactNode): string {
  return textOf(children)
    .toLowerCase()
    .replace(/[^a-z0-9\s-]/g, "")
    .trim()
    .replace(/[\s-]+/g, "-");
}

/* A section heading carries its id and an anchor to itself, so a reader can
   hand someone the section rather than the page. The anchor follows the text
   in the DOM and sits in the margin visually; it names itself for assistive
   tech, since "#" alone says nothing. */
function heading(Tag: "h2" | "h3") {
  return function Heading({ children }: { children?: ReactNode }) {
    const id = headingId(children);
    return (
      <Tag id={id}>
        {children}
        <a className={styles.anchor} href={`#${id}`} aria-label="Link to this section">
          #
        </a>
      </Tag>
    );
  };
}

/* Element overrides shared by every markdown page. Everything markdown renders
   is styled by prose.module.css against the plain tag; only <pre> needs a
   component, because a code block is a control — you copy it — rather than
   text, and the two section heading levels, because a heading is a place a
   link can land.

   The <pre> override runs on the server while the page renders, which is
   where the block is tokenized; the Snippet it returns is the client half
   and only ever sees the tokens.

   What the fence's opening line says — the language, a variant after a
   colon, a title in the meta — is read by parseFence(). */
export const markdownComponents: MDXComponents = {
  h2: heading("h2"),
  h3: heading("h3"),
  pre: async ({ children }) => {
    const block = children as CodeElement;
    const code = String(block?.props?.children ?? "").replace(/\n+$/, "");
    const fence = parseFence(block?.props?.className, block?.props?.meta);
    const lines = await highlight(code, fence.language, fence.shell);
    return (
      <Snippet
        code={code}
        lines={lines}
        shell={fence.shell}
        variant={fence.variant}
        title={fence.title}
        kind={kindOf(fence)}
        numbered={fence.numbered}
      />
    );
  },
};
