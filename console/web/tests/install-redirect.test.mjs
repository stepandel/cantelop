import { describe, expect, test } from "bun:test";

import nextConfig from "../next.config.ts";

describe("curl installer redirect", () => {
  test("keeps the public console URL on the current tap installer", async () => {
    const redirects = await nextConfig.redirects?.();

    expect(redirects).toContainEqual({
      source: "/install.sh",
      destination: "https://raw.githubusercontent.com/stepandel/homebrew-tap/main/install.sh",
      permanent: false,
    });
  });
});
