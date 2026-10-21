import { expect, test } from "bun:test";
import { markdownSources } from "../components/markdown/source.ts";
import { DOCS_ORIGIN, docsMarkdown, llmsFullText, llmsIndex } from "../lib/docs-markdown.ts";
import { GET, generateStaticParams } from "../app/docs/[slug]/route.ts";

test("every public doc is indexed, bundled, and available as Markdown", async () => {
  const pages = docsMarkdown();
  const index = llmsIndex();
  const full = llmsFullText();
  expect(pages.map(({ path }) => path).sort()).toEqual(
    Object.keys(markdownSources("web/app/docs", "/docs")).sort(),
  );
  expect(index.startsWith("# Cantelop\n\n> ")).toBe(true);
  expect(generateStaticParams()).toHaveLength(pages.length);
  for (const page of pages) {
    expect(index).toContain(`](${page.url})`);
    expect(full).toContain(`Source: ${page.url}\n\n${page.markdown}`);
    const response = await GET(new Request(page.url), {
      params: Promise.resolve({ slug: page.path.slice("/docs/".length) + ".md" }),
    });
    expect(response.status).toBe(200);
    expect(response.headers.get("Content-Type")).toBe("text/markdown; charset=utf-8");
    expect(await response.text()).toBe(page.markdown + "\n");
  }
});

test("Markdown preserves SDK imports in code and drops page metadata", () => {
  const pages = docsMarkdown();
  expect(pages.find(({ path }) => path === "/docs/ingress").markdown)
    .toContain('import {');
  expect(llmsFullText()).not.toContain("export const metadata");
});

test("the Markdown route rejects unknown and non-document paths", async () => {
  for (const slug of ["unknown.md", "architecture", "../../.env", "../profile.md"]) {
    const response = await GET(new Request(`${DOCS_ORIGIN}/docs/unknown.md`), {
      params: Promise.resolve({ slug }),
    });
    expect(response.status).toBe(404);
  }
});
