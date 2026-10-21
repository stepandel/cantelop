import { expect, test } from "bun:test";

import { ConfigCipher } from "./cipher";

test("encrypts secrets with App-and-name authenticated context", () => {
  const cipher = new ConfigCipher(Buffer.alloc(32, 7));
  const sealed = cipher.seal("app_0123456789abcdef0123456789abcdef", "API_TOKEN", "never-store-plaintext");
  expect(sealed.nonce).toHaveLength(12);
  expect(sealed.ciphertext.includes(Buffer.from("never-store-plaintext"))).toBe(false);
  expect(cipher.open("app_0123456789abcdef0123456789abcdef", "API_TOKEN", sealed.nonce, sealed.ciphertext))
    .toBe("never-store-plaintext");
  expect(() => cipher.open("app_0123456789abcdef0123456789abcdef", "OTHER", sealed.nonce, sealed.ciphertext)).toThrow();
});
