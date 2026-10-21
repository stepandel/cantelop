import { resolve } from "node:path";
import createMDX from "@next/mdx";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  poweredByHeader: false,
  // The schema generator loads optional database drivers at runtime.
  serverExternalPackages: ["drizzle-kit"],
  // Docs pages are authored as markdown; every other route stays .tsx.
  pageExtensions: ["ts", "tsx", "mdx"],
  turbopack: { root: process.cwd() },
  // /docs is a wrapper segment with no page of its own; the section opens on
  // its first child. Temporary, so the landing page can change later without
  // a permanent redirect cached in browsers.
  async redirects() {
    return [
      {
        source: "/:path*",
        has: [{ type: "host", value: "www.cantelop.com" }],
        destination: "https://cantelop.com/:path*",
        permanent: true,
      },
      { source: "/docs", destination: "/docs/quick-start", permanent: false },
      { source: "/docs/sdk-1", destination: "/docs/quick-start", permanent: true },
      { source: "/docs/sdk-1.md", destination: "/docs/quick-start.md", permanent: true },
      { source: "/docs/sessions-and-messages", destination: "/docs/session-runtime", permanent: true },
      // SDK 1.0 generates the App's Edge Worker; there is no customer Edge API.
      { source: "/docs/edge-api", destination: "/docs/ingress", permanent: true },
      { source: "/docs/edge-api.md", destination: "/docs/ingress.md", permanent: true },
      // The agents API example predates SDK 1.0 and is no longer published.
      { source: "/guides/agents-api", destination: "/guides", permanent: false },
      {
        source: "/guides/:path*",
        has: [{ type: "host", value: "console.cantelop.dev" }],
        destination: "https://cantelop.com/guides/:path*",
        permanent: true,
      },
      {
        source: "/docs/session-runtime-configuration",
        destination: "/docs/session-runtime#runtime-configuration",
        permanent: false,
      },
      {
        source: "/install.sh",
        destination: "https://raw.githubusercontent.com/stepandel/homebrew-tap/main/install.sh",
        permanent: false,
      },
    ];
  },
  async headers() {
    return [
      {
        source: "/docs/:slug([a-z0-9-]+)",
        headers: [{
          key: "Link",
          value: '</docs/:slug.md>; rel="alternate"; type="text/markdown", </llms.txt>; rel="describedby"',
        }],
      },
      {
        source: "/:path*",
        headers: [
          { key: "Content-Security-Policy", value: "frame-ancestors 'none'" },
          { key: "X-Frame-Options", value: "DENY" },
        ],
      },
    ];
  },
};

export default createMDX({
  options: {
    // By path, not by import: Turbopack takes MDX plugins as strings for the
    // loader to resolve. The plugin hands a fence's meta (```ts title="…")
    // to the markdown components; see components/markdown/fence.ts.
    rehypePlugins: [resolve("web/components/markdown/rehype-fence-meta.mjs")],
  },
})(nextConfig);
