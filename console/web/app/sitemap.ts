import type { MetadataRoute } from "next";
import { POSTS } from "./blog/posts";
import { GUIDES } from "./guides/guides";
import { docsMarkdown } from "@/lib/docs-markdown";

const origin = "https://cantelop.com";

export default function sitemap(): MetadataRoute.Sitemap {
  const paths = [
    "/",
    "/contact",
    "/privacy",
    "/blog",
    ...POSTS.map(({ slug }) => `/blog/${slug}`),
    "/guides",
    ...GUIDES.map(({ slug }) => `/guides/${slug}`),
    ...docsMarkdown().map(({ path }) => path),
  ];

  return paths.map((path) => ({ url: new URL(path, origin).toString() }));
}
