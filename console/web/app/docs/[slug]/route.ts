import { docsMarkdown, docsTextResponse } from "../../../lib/docs-markdown";

export const dynamic = "force-static";
export const dynamicParams = false;

export function generateStaticParams() {
  return docsMarkdown().map(({ path }) => ({ slug: `${path.slice("/docs/".length)}.md` }));
}

export async function GET(_request: Request, { params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  const page = docsMarkdown().find(({ path }) => `${path}.md` === `/docs/${slug}`);
  if (!page) return new Response("Not found\n", { status: 404 });
  return docsTextResponse(`${page.markdown}\n`);
}
