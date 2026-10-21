import { expect, test } from "bun:test";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, relative } from "node:path";

import { markdownSources } from "../components/markdown/source.ts";

test("copyable markdown preserves complete code examples while removing MDX chrome", () => {
  const directory = mkdtempSync(join(tmpdir(), "cantelop-markdown-"));
  const example = [
    'import { CantelopClient } from "@cantelop/sdk";',
    'export const app = new CantelopClient().app({ name: "hello", runtime });',
    '<Example />',
  ].join("\n");
  try {
    mkdirSync(join(directory, "guide"));
    writeFileSync(join(directory, "guide", "page.mdx"), [
      'import { Next } from "@/components/markdown";',
      'export const metadata = { title: "Guide" };',
      '',
      '# guide',
      '',
      '```tsx',
      example,
      '```',
      '',
      '<Next href="/docs/cli">cli reference →</Next>',
    ].join("\n"));

    expect(markdownSources(relative(process.cwd(), directory), "/docs")["/docs/guide"])
      .toBe(`# guide\n\n\x60\x60\x60tsx\n${example}\n\x60\x60\x60`);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
