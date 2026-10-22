import { expect, test } from "bun:test";

import { fleetCapacitySpec, fleetCreateSpec, principalSpec } from "../lib/fleet-input.ts";

test("creates a Fleet with a finite or an explicitly unlimited cap", () => {
  expect(fleetCreateSpec({ name: "customer-agents", max_apps: 50 })).toEqual({ name: "customer-agents", maxApps: 50 });
  expect(fleetCreateSpec({ name: "a", max_apps: null })).toEqual({ name: "a", maxApps: null });
});

test("refuses a missing cap, a bad cap, a bad name or an unknown key", () => {
  expect(() => fleetCreateSpec({ name: "agents" })).toThrow();
  for (const max_apps of [0, 10_001, 1.5, "10", undefined]) {
    expect(() => fleetCreateSpec({ name: "agents", max_apps })).toThrow();
  }
  for (const name of ["", "1agents", "Agents", "a_b", "a".repeat(64), 7]) {
    expect(() => fleetCreateSpec({ name, max_apps: 1 })).toThrow();
  }
  expect(() => fleetCreateSpec({ name: "agents", max_apps: 1, owner: "usr_x" })).toThrow();
  expect(() => fleetCreateSpec(null)).toThrow();
  expect(() => fleetCreateSpec([])).toThrow();
});

test("changes the cap, including to unlimited", () => {
  expect(fleetCapacitySpec({ max_apps: 10_000 })).toEqual({ maxApps: 10_000 });
  expect(fleetCapacitySpec({ max_apps: null })).toEqual({ maxApps: null });
  expect(() => fleetCapacitySpec({})).toThrow();
  expect(() => fleetCapacitySpec({ max_apps: 5, name: "x" })).toThrow();
});

test("turns a principal's lifetime in days into its expiry", () => {
  const now = Date.parse("2026-10-21T12:00:00Z");
  expect(principalSpec({ label: "  provisioning  ", scopes: ["installations:read"], expires_in_days: 90 }, now)).toEqual({
    label: "provisioning",
    scopes: ["installations:read"],
    expiresAt: "2027-01-19T12:00:00.000Z",
  });
  expect(principalSpec({ label: "max", scopes: ["usage:read"], expires_in_days: 366 }, now).expiresAt)
    .toBe("2027-10-22T12:00:00.000Z");
});

test("refuses an empty label, unknown or repeated scopes, and an out-of-range lifetime", () => {
  const valid = { label: "worker", scopes: ["policy:write"], expires_in_days: 30 };
  expect(() => principalSpec({ ...valid, label: "   " })).toThrow();
  expect(() => principalSpec({ ...valid, label: "x".repeat(129) })).toThrow();
  expect(() => principalSpec({ ...valid, scopes: [] })).toThrow();
  expect(() => principalSpec({ ...valid, scopes: ["tenant:write"] })).toThrow();
  expect(() => principalSpec({ ...valid, scopes: ["policy:write", "policy:write"] })).toThrow();
  for (const expires_in_days of [0, 367, 1.5, "30"]) {
    expect(() => principalSpec({ ...valid, expires_in_days })).toThrow();
  }
  expect(() => principalSpec({ ...valid, expires_at: "2027-01-01T00:00:00Z" })).toThrow();
});
