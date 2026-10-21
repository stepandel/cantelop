import { expect, test } from "bun:test";

import { accessLabel } from "../lib/app-access.ts";

test("an owner reads their admin count", () => {
  expect(accessLabel({ role: "owner", admin_count: 0 })).toBe("Owner · no admins");
  expect(accessLabel({ role: "owner", admin_count: 1 })).toBe("Owner · 1 admin");
  expect(accessLabel({ role: "owner", admin_count: 3 })).toBe("Owner · 3 admins");
});

test("an admin reads whose app it is", () => {
  expect(accessLabel({ role: "admin", owner: { email: "maya@example.com" }, admin_count: 2 })).toBe("Admin · owned by maya@example.com");
});

test("an admin without an owner record still reads as an admin", () => {
  expect(accessLabel({ role: "admin", admin_count: 2 })).toBe("Admin · owned by another account");
});
