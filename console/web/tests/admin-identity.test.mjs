import { expect, test } from "bun:test";

import { isVerifiedAdminSession } from "../lib/admin-identity.ts";

const user = { id: `usr_${"a".repeat(32)}`, login: "admin", email: "admin@example.com" };

test("admin requires the provider-verified email, not the product contact email", () => {
  expect(isVerifiedAdminSession({ user: { ...user, verifiedEmail: "changed@example.com" } }, "admin@example.com")).toBe(false);
  expect(isVerifiedAdminSession({ user }, "admin@example.com")).toBe(false);
  expect(isVerifiedAdminSession({ user: { ...user, verifiedEmail: " ADMIN@example.com " } }, "admin@example.com")).toBe(true);
});

test("admin rejects unavailable identity and missing configuration", () => {
  expect(isVerifiedAdminSession(null, "admin@example.com")).toBe(false);
  expect(isVerifiedAdminSession({ user: { ...user, verifiedEmail: "admin@example.com" } }, undefined)).toBe(false);
  expect(isVerifiedAdminSession({ user: { ...user, login: null, verifiedEmail: "admin@example.com" } }, "admin@example.com")).toBe(false);
});
