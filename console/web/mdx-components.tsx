import type { MDXComponents } from "mdx/types";

import { markdownComponents } from "@/components/markdown";

/* Next resolves this file by name and location, so it stays a thin seam: the
   overrides themselves live with the rest of the markdown layer. */
export function useMDXComponents(components: MDXComponents): MDXComponents {
  return { ...components, ...markdownComponents };
}
