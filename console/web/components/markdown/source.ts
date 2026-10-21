import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

/* Reads the markdown a section is written in, so a page can hand a reader the
   source rather than the rendering. Server-only, and deliberately not exported
   from the package index: it runs while the static pages are prerendered, and
   nothing should pull node:fs toward a client bundle.

   `next dev|build|start web` all run from console/, so paths are relative to
   that. */
export function markdownSources(directory: string, urlBase: string): Record<string, string> {
  const sources: Record<string, string> = {};

  for (const entry of readdirSync(join(process.cwd(), directory), { withFileTypes: true })) {
    if (!entry.isDirectory()) {
      continue;
    }
    const route = `${urlBase}/${entry.name}`;
    const page = join(process.cwd(), directory, entry.name, "page.mdx");
    try {
      sources[route] = plainMarkdown(readFileSync(page, "utf8"));
    } catch {
      // A segment without a page.mdx is a route of some other kind; a section
      // can mix them, and only the markdown ones offer their source.
      Object.assign(sources, markdownSources(join(directory, entry.name), route));
    }
  }

  return sources;
}

/* MDX is markdown plus a little JSX. The JSX is how the page renders, not what
   it says, so the source a reader copies is the markdown with those lines
   removed: the ESM header, and any line that is only a component tag. Our
   components are capitalised, which is what separates them from the literal
   `<workspace>--<app>` style text the prose itself uses. Fenced examples keep
   their imports, exports, and JSX so copied code remains complete. */
function plainMarkdown(mdx: string): string {
  let fence: string | undefined;
  return mdx
    .split("\n")
    .filter((line) => {
      const trimmed = line.trim();
      const marker = /^(\x60{3,}|~{3,})/.exec(trimmed)?.[1];
      if (fence) {
        if (marker?.[0] === fence[0] && marker.length >= fence.length && trimmed === marker) {
          fence = undefined;
        }
        return true;
      }
      if (marker) {
        fence = marker;
        return true;
      }
      if (/^(import|export)\s/.test(trimmed)) {
        return false;
      }
      return !(/^<\/?[A-Z]/.test(trimmed) && trimmed.endsWith(">"));
    })
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}
