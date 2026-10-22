import { expect, test } from "bun:test";

import { capacityLabel, fleetErrorMessage, formatCredits, formatStorage, principalState } from "../lib/fleet-labels.ts";

test("reads a cap as a count or as unlimited", () => {
  expect(capacityLabel(12, 50)).toBe("12 / 50");
  expect(capacityLabel(1284, null)).toBe("1284 / ∞");
});

test("a principal is revoked, expired or active, in that order", () => {
  const now = Date.parse("2026-10-21T12:00:00Z");
  expect(principalState({ expires_at: "2027-01-01T00:00:00Z", revoked_at: null }, now)).toBe("active");
  expect(principalState({ expires_at: "2026-10-21T12:00:00Z", revoked_at: null }, now)).toBe("expired");
  expect(principalState({ expires_at: "2026-01-01T00:00:00Z", revoked_at: "2025-12-01T00:00:00Z" }, now)).toBe("revoked");
});

test("names the quota error and falls back for the unknown", () => {
  expect(fleetErrorMessage("quota_exceeded")).toContain("app slot");
  expect(fleetErrorMessage(undefined)).toBe("The control service could not complete that request");
});

test("formats storage in binary units and credits without losing digits", () => {
  expect(formatStorage(0)).toBe("0");
  expect(formatStorage(50 * 1024 ** 3)).toBe("50 gib");
  expect(formatStorage(1536 * 1024 ** 2)).toBe("1.5 gib");
  expect(formatStorage(1024 ** 4)).toBe("1 tib");
  expect(formatCredits("1832250")).toBe("1,832,250");
  expect(formatCredits("9223372036854775807")).toBe("9,223,372,036,854,775,807");
  expect(formatCredits("0")).toBe("0");
});
