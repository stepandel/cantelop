import { expect, test } from "bun:test";

import { coversWindow, mergeEvents } from "../lib/request-history.ts";

const at = (iso, sequence) => ({
  sequence,
  event_id: `e${sequence}`,
  workspace_id: "wsp_1",
  received_at: iso,
  status_code: 200,
  duration_ms: 12,
});

test("merge keeps one entry per sequence, newest first", () => {
  const merged = mergeEvents(
    [at("2026-08-04T09:00:00Z", 3), at("2026-08-04T08:00:00Z", 2)],
    [at("2026-08-04T10:00:00Z", 4), at("2026-08-04T08:00:00Z", 2)],
  );
  expect(merged.map(({ sequence }) => sequence)).toEqual([4, 3, 2]);
});

test("merge is stable when a page repeats what is already loaded", () => {
  const loaded = [at("2026-08-04T09:00:00Z", 3)];
  expect(mergeEvents(loaded, loaded).map(({ sequence }) => sequence)).toEqual([3]);
});

test("coverage is false until an event predates the window start", () => {
  const windowStart = Date.parse("2026-08-04T06:00:00Z");
  expect(coversWindow([], windowStart)).toBe(false);
  expect(coversWindow([at("2026-08-04T09:00:00Z", 3), at("2026-08-04T07:00:00Z", 2)], windowStart)).toBe(false);
  expect(coversWindow([at("2026-08-04T09:00:00Z", 3), at("2026-08-04T05:00:00Z", 1)], windowStart)).toBe(true);
});

test("an event exactly at the window start counts as covering it", () => {
  const windowStart = Date.parse("2026-08-04T06:00:00Z");
  expect(coversWindow([at("2026-08-04T06:00:00Z", 1)], windowStart)).toBe(true);
});

test("an unparseable oldest timestamp is not treated as coverage", () => {
  expect(coversWindow([at("not a date", 1)], Date.parse("2026-08-04T06:00:00Z"))).toBe(false);
});
