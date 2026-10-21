import { expect, test } from "bun:test";

import { maxConfigMutationRequestBytes, maxConfigValueBytes } from "./limits.ts";

test("the configuration request bound contains the worst-case valid JSON value", () => {
  const value = "\u0001".repeat(maxConfigValueBytes);
  const encoded = JSON.stringify({ value });

  expect(Buffer.byteLength(value)).toBe(maxConfigValueBytes);
  expect(Buffer.byteLength(encoded)).toBeGreaterThan(66 * 1024);
  expect(Buffer.byteLength(encoded)).toBeLessThanOrEqual(maxConfigMutationRequestBytes);
});
