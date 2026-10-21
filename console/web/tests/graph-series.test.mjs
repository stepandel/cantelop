import { expect, test } from "bun:test";

import {
  buildWindow,
  lifetimeHistogram,
  niceMax,
  percentile,
  requestLatency,
  requestVolume,
  sandboxConcurrency,
  sessionStarts,
  startupPoints,
} from "../lib/graph-series.ts";

const OBSERVED_AT = "2026-08-04T12:00:00Z";
const observed = Date.parse(OBSERVED_AT);
const minutes = (count) => count * 60 * 1_000;

// A one-hour window in six ten-minute buckets keeps the expected counts
// countable by hand.
const window = buildWindow(OBSERVED_AT, minutes(60), 6);

function sandbox(overrides) {
  return {
    id: "sbx_1",
    workspace_id: "wsp_1",
    app_id: "app_1",
    profile: "standard-2",
    build_id: "sha256:abc",
    vcpus: 2,
    memory_mib: 2048,
    state: "terminated",
    created_at: OBSERVED_AT,
    updated_at: OBSERVED_AT,
    runtime_timing: {
      status: overrides.state === "running" ? "running" : "stopped",
      started_at: overrides.created_at ?? OBSERVED_AT,
      ...(overrides.state === "running" ? {} : { stopped_at: overrides.updated_at ?? OBSERVED_AT }),
    },
    ...overrides,
  };
}

test("window ends at the observation, not the browser clock", () => {
  expect(window.end).toBe(observed);
  expect(window.start).toBe(observed - minutes(60));
  expect(window.bucketMS).toBe(minutes(10));
});

test("concurrency counts a terminal sandbox only inside its interval", () => {
  const points = sandboxConcurrency(
    [sandbox({
      startup: { completed_at: new Date(observed - minutes(49)).toISOString(), duration_ms: minutes(1) },
      created_at: new Date(observed - minutes(50)).toISOString(),
      updated_at: new Date(observed - minutes(35)).toISOString(),
    })],
    OBSERVED_AT,
    window,
  );
  // Bucket edges land at T-50, T-40, T-30, … so the record is alive at the
  // first two and gone by the third.
  expect(points.map(({ value }) => value)).toEqual([1, 1, 0, 0, 0, 0]);
});

test("concurrency holds a live sandbox open to the observation", () => {
  const points = sandboxConcurrency(
    [sandbox({ state: "running", created_at: new Date(observed - minutes(25)).toISOString(), updated_at: new Date(observed - minutes(24)).toISOString() })],
    OBSERVED_AT,
    window,
  );
  expect(points.map(({ value }) => value)).toEqual([0, 0, 0, 1, 1, 1]);
});

test("concurrency ignores records whose timestamps do not parse", () => {
  const points = sandboxConcurrency([sandbox({ created_at: "not a date" })], OBSERVED_AT, window);
  expect(points.every(({ value }) => value === 0)).toBe(true);
});

test("concurrency excludes legacy attempts without measured runtime timing", () => {
  const points = sandboxConcurrency(
    [
      sandbox({
        id: "failed-before-ready",
        runtime_timing: undefined,
        state: "failed",
        failure: { reason: "runtime_start_failed" },
        created_at: new Date(observed - minutes(50)).toISOString(),
        updated_at: new Date(observed - minutes(10)).toISOString(),
      }),
      sandbox({
        id: "started-then-terminated",
        startup: { completed_at: new Date(observed - minutes(48)).toISOString(), duration_ms: minutes(2) },
        created_at: new Date(observed - minutes(50)).toISOString(),
        updated_at: new Date(observed - minutes(35)).toISOString(),
      }),
    ],
    OBSERVED_AT,
    window,
  );
  expect(points.map(({ value }) => value)).toEqual([1, 1, 0, 0, 0, 0]);
});

test("session starts count arrivals in their bucket and drop anything older than the window", () => {
  const session = (createdAt) => ({
    id: `ses_${createdAt}`,
    workspace_id: "wsp_1",
    app_id: "app_1",
    build_id: "sha256:abc",
    keep_alive_seconds: 600,
    state: "idle",
    created_at: new Date(createdAt).toISOString(),
    updated_at: new Date(createdAt).toISOString(),
  });
  const points = sessionStarts(
    [session(observed - minutes(55)), session(observed - minutes(52)), session(observed - minutes(5)), session(observed - minutes(90))],
    window,
  );
  expect(points.map(({ value }) => value)).toEqual([2, 0, 0, 0, 0, 1]);
});

test("request volume splits a bucket by status class", () => {
  const event = (statusCode, minutesBack) => ({
    sequence: 1,
    event_id: `e${statusCode}${minutesBack}`,
    workspace_id: "wsp_1",
    received_at: new Date(observed - minutes(minutesBack)).toISOString(),
    status_code: statusCode,
    duration_ms: 10,
  });
  const buckets = requestVolume([event(200, 5), event(503, 5), event(404, 5), event(301, 45)], window);
  expect(buckets[5].total).toBe(3);
  expect(buckets[5].counts).toEqual({ "2xx": 1, "3xx": 0, "4xx": 1, "5xx": 1 });
  expect(buckets[1].counts["3xx"]).toBe(1);
});

test("latency reports nearest-rank percentiles per bucket and nothing for empty ones", () => {
  const event = (durationMS) => ({
    sequence: 1,
    event_id: `e${durationMS}`,
    workspace_id: "wsp_1",
    received_at: new Date(observed - minutes(5)).toISOString(),
    status_code: 200,
    duration_ms: durationMS,
  });
  const buckets = requestLatency([event(10), event(20), event(30), event(400)], window);
  expect(buckets[5].count).toBe(4);
  expect(buckets[5].p50).toBe(20);
  expect(buckets[5].p95).toBe(400);
  expect(buckets[0].count).toBe(0);
  expect(buckets[0].p50).toBeUndefined();
});

test("percentile handles the single-sample and empty cases", () => {
  expect(percentile([], 0.5)).toBeUndefined();
  expect(percentile([7], 0.95)).toBe(7);
  expect(percentile([1, 2, 3, 4], 0.5)).toBe(2);
});

test("startup points take the ready instant, and a failed boot contributes none", () => {
  const points = startupPoints(
    [
      sandbox({ id: "sbx_ok", startup: { completed_at: new Date(observed - minutes(12)).toISOString(), duration_ms: 1_400 } }),
      sandbox({ id: "sbx_old", startup: { completed_at: new Date(observed - minutes(120)).toISOString(), duration_ms: 900 } }),
      sandbox({ id: "sbx_failed", state: "failed" }),
    ],
    window,
  );
  expect(points.map(({ id }) => id)).toEqual(["sbx_ok"]);
  expect(points[0].durationMS).toBe(1_400);
});

test("the lifetime histogram excludes still-growing records instead of binning them short", () => {
  const { bins, sampled, live } = lifetimeHistogram([
    sandbox({ id: "a", startup: { completed_at: new Date(observed - minutes(59)).toISOString(), duration_ms: minutes(1) }, created_at: new Date(observed - minutes(60)).toISOString(), updated_at: new Date(observed - minutes(59)).toISOString() }),
    sandbox({ id: "b", runtime_timing: { status: "not_started" }, state: "failed", failure: { reason: "runtime_start_failed" }, created_at: new Date(observed - minutes(60)).toISOString(), updated_at: new Date(observed - minutes(60) + 5_000).toISOString() }),
    sandbox({ id: "c", state: "running", created_at: new Date(observed - minutes(600)).toISOString(), updated_at: OBSERVED_AT }),
  ]);
  expect(sampled).toBe(1);
  expect(live).toBe(1);
  expect(bins[1].count).toBe(1); // the 60s teardown
  expect(bins.reduce((total, bin) => total + bin.count, 0)).toBe(1);
});

test("nice axis maxima round up to a readable step", () => {
  expect(niceMax(0, 2)).toBe(2);
  expect(niceMax(3)).toBe(5);
  expect(niceMax(7)).toBe(10);
  expect(niceMax(1_100, 100)).toBe(2_000);
});
