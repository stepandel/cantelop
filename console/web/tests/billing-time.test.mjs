import { expect, test } from "bun:test";
import { appBillingSummary, sandboxRuntimeDuration, sandboxRuntimeInterval } from "../lib/instances.ts";
import { billingTimeLabel, billingTimeTitle } from "../lib/billing-time.ts";
import { lifetimeHistogram, sandboxConcurrency, buildWindow } from "../lib/graph-series.ts";

// A complete total is unavailable when any historical interval is unknown.
function appBillingDuration(snapshot, appId) {
  const summary = appBillingSummary(snapshot, appId);
  return summary && summary.unknown === 0 ? summary.durationMS : undefined;
}

const observed_at = "2026-09-21T12:10:00Z";
const start = "2026-09-21T12:00:00Z";
const end = "2026-09-21T12:01:00Z";
const sandbox = (overrides = {}) => ({
  app_id: "app_one", state: "running", created_at: start, updated_at: end,
  runtime_timing: { status: "running", started_at: start }, ...overrides,
});
const stopped = (overrides = {}) => sandbox({
  state: "terminated", runtime_timing: { status: "stopped", started_at: start, stopped_at: end }, ...overrides,
});

test("billing sums concurrent measured intervals and isolates apps", () => {
  const snapshot = { observed_at, sandboxes: [sandbox(), sandbox(), stopped(), sandbox({ app_id: "app_other" })] };
  expect(appBillingDuration(snapshot, "app_one")).toBe(1_260_000);
  expect(appBillingDuration({ ...snapshot, observed_at: "2026-09-21T12:11:00Z" }, "app_one")).toBe(1_380_000);
});

test("cleanup hours after exit cannot change uptime, billing, or histogram", () => {
  const before = stopped({ state: "failed" });
  const after = { ...before, state: "terminated", updated_at: "2026-09-21T13:02:00Z" };
  expect(sandboxRuntimeDuration(before, observed_at)).toBe(60_000);
  expect(sandboxRuntimeDuration(after, observed_at)).toBe(60_000);
  expect(appBillingDuration({ observed_at, sandboxes: [after] }, "app_one")).toBe(60_000);
  expect(lifetimeHistogram([after])).toEqual(lifetimeHistogram([before]));
  expect(lifetimeHistogram([after]).sampled).toBe(1); // includes measured startup failure
});

test("unknown historical timing stays unknown even with a successful startup", () => {
  const legacy = sandbox({ state: "terminated", runtime_timing: undefined, startup: { duration_ms: 200, completed_at: end } });
  const snapshot = { observed_at, sandboxes: [legacy, stopped()] };
  expect(sandboxRuntimeDuration(legacy, observed_at)).toBeUndefined();
  expect(appBillingDuration(snapshot, "app_one")).toBeUndefined();
  const summary = appBillingSummary(snapshot, "app_one");
  expect(summary).toEqual({ durationMS: 60_000, measured: 1, unknown: 1 });
  expect(billingTimeLabel(summary)).toBe("≥ 1m");
  expect(billingTimeTitle(summary)).toContain("Incomplete history");
  expect(billingTimeLabel(appBillingSummary({ observed_at, sandboxes: [legacy] }, "app_one"))).toBe("Incomplete");
  expect(lifetimeHistogram([legacy]).unknown).toBe(1);
});

test("zero usage, unavailable telemetry, and interrupted measurement remain distinct", () => {
  expect(appBillingDuration(null, "app_one")).toBeUndefined();
  expect(billingTimeLabel(undefined)).toBe("—");
  expect(appBillingDuration({ observed_at, sandboxes: [] }, "app_one")).toBe(0);
  expect(sandboxRuntimeDuration(sandbox({ runtime_timing: { status: "not_started" }, state: "failed" }), observed_at)).toBe(0);
  expect(sandboxRuntimeDuration(sandbox({ runtime_timing: { status: "pending" }, state: "starting" }), observed_at)).toBe(0);
  for (const runtime_timing of [undefined, { status: "unknown", started_at: start }, { status: "running", started_at: start }, { status: "pending" }]) {
    expect(sandboxRuntimeDuration(sandbox({ runtime_timing, state: "terminated" }), observed_at)).toBeUndefined();
  }
});

test("invalid measured timestamps are not zero and cannot contribute to totals", () => {
  for (const started_at of ["invalid", "2026-09-22T12:00:00Z"]) {
    const record = sandbox({ runtime_timing: { status: "running", started_at } });
    expect(sandboxRuntimeDuration(record, observed_at)).toBeUndefined();
  }
  expect(sandboxRuntimeDuration(stopped({ runtime_timing: { status: "stopped", started_at: start } }), observed_at)).toBeUndefined();
});

test("concurrency stops at process exit even while lifecycle is still stopping", () => {
  const record = stopped({ state: "stopping" });
  expect(sandboxRuntimeInterval(record, observed_at)).toEqual({ start: Date.parse(start), end: Date.parse(end), terminal: true });
  const window = buildWindow(observed_at, 10 * 60_000, 10);
  expect(sandboxConcurrency([record], observed_at, window).every(({ value }) => value === 0)).toBe(true);
});

test("an open interval on a terminal record is a lost exit write, not live billing", () => {
  for (const state of ["failed", "terminated"]) {
    const record = sandbox({ state });
    expect(sandboxRuntimeDuration(record, observed_at)).toBeUndefined();
    expect(lifetimeHistogram([record]).unknown).toBe(1);
  }
});
