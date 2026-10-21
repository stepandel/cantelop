/* Markdown pages.
 *
 * A page is a single .mdx file. To add one, write `app/<route>/page.mdx` and
 * give the route a layout that wraps its children in <Prose>:
 *
 *     import { Prose } from "@/components/markdown";
 *
 *     export default function Layout({ children }) {
 *       return <main>{yourChrome}<Prose>{children}</Prose></main>;
 *     }
 *
 * The markdown then styles itself — headings, lists, blockquotes, rules — and
 * fenced code blocks render as copyable, syntax-highlighted snippets (the
 * grammars ./highlight.ts knows: shell, ts, tsx, json, dockerfile; anything
 * else is plain). A block is a panel with a strip along its top that names
 * it and holds the copy control. Tag a block `shell` for a terminal
 * transcript: lines starting with "$ " are commands, lines starting with
 * "→ " are output, and copying takes the commands alone. A block that shows
 * a file names it in the fence's meta, and its lines are numbered:
 *
 *     ```ts title="src/cantelop.ts"
 *
 * Three components cover what markdown cannot express; import them in the
 * .mdx file that needs them.
 *
 *   <Checks>   a list that means ticks rather than bullets
 *   <Next>     a closing link that is a control, not a link in a sentence
 *   <Variants> one instruction in several forms. Each child is a fence
 *              labelled after its language — ```shell:openai — and blocks
 *              sharing a group stay in step across the whole page.
 *
 * A section's layout can also offer <CopyPage>, which hands the reader the
 * page's markdown for pasting into a model. Pair it with markdownSources()
 * from ./source, which is server-only and not exported here.
 */
export { Prose } from "./prose";
export { CopyPage } from "./copy-page";
export { Snippet } from "./snippet";
export { Checks, Next } from "./blocks";
export { Variants } from "./variants";
export { markdownComponents } from "./components";
