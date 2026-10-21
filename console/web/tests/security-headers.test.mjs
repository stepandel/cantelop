import { describe, expect, test } from "bun:test";

import nextConfig from "../next.config.ts";

describe("security headers", () => {
  test("deny framing on every route", async () => {
    const rules = (await nextConfig.headers?.()) ?? [];
    const catchAll = rules.find((rule) => rule.source === "/:path*");
    expect(catchAll).toBeDefined();
    expect(catchAll?.headers).toContainEqual({
      key: "Content-Security-Policy",
      value: "frame-ancestors 'none'",
    });
    expect(catchAll?.headers).toContainEqual({ key: "X-Frame-Options", value: "DENY" });
  });

  test("advertise markdown alternates for docs pages", async () => {
    const rules = (await nextConfig.headers?.()) ?? [];
    const docs = rules.find((rule) => rule.source === "/docs/:slug([a-z0-9-]+)");
    expect(docs).toBeDefined();
    const link = docs?.headers.find((header) => header.key === "Link");
    expect(link?.value).toContain('</docs/:slug.md>; rel="alternate"; type="text/markdown"');
    expect(link?.value).toContain('</llms.txt>; rel="describedby"');
  });
});
