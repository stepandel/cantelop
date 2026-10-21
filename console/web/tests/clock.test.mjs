import { expect, test } from "bun:test";

import { clockFor } from "../lib/format.ts";

// 2026-08-04T09:41:52Z is 02:41:52 in Los Angeles (PDT) and 11:41:52 in
// Berlin — one instant that lands on two clocks, which is the whole point.
const INSTANT = "2026-08-04T09:41:52Z";

test("a reading is drawn on the viewer's own clock", () => {
  expect(clockFor("UTC").formatTime(INSTANT)).toBe("09:41:52");
  expect(clockFor("America/Los_Angeles").formatTime(INSTANT)).toBe("02:41:52");
  expect(clockFor("Europe/Berlin").formatTime(INSTANT)).toBe("11:41:52");
});

test("the stamp carries the date its own zone is on", () => {
  // 06:02:41Z is still Aug 3 in Los Angeles: the date has to follow the clock,
  // or a row reads as a day it did not happen on.
  expect(clockFor("UTC").formatStamp("2026-08-04T06:02:41Z")).toBe("08-04 06:02:41");
  expect(clockFor("America/Los_Angeles").formatStamp("2026-08-04T06:02:41Z")).toBe("08-03 23:02:41");
});

test("the full reading names the zone it is drawn in", () => {
  // The separator between date and time is the engine's business ("," or
  // "at"); the parts are ours.
  expect(clockFor("America/Los_Angeles").formatDate(INSTANT)).toMatch(/^Aug 4, 2026\D+02:41:52 PDT$/);
  expect(clockFor("America/Los_Angeles").formatZone(INSTANT)).toBe("PDT");
});

test("midnight reads as 00, never 24", () => {
  expect(clockFor("UTC").formatTime("2026-08-04T00:00:00Z")).toBe("00:00:00");
  expect(clockFor("UTC").formatStamp("2026-08-04T00:00:00Z")).toBe("08-04 00:00:00");
});

test("days are compared in the viewer's zone", () => {
  const pacific = clockFor("America/Los_Angeles");
  expect(pacific.dayOf(INSTANT)).toBe("2026-08-04");
  // 08-04T06:02Z and 08-04T09:41Z share a UTC day but not a Pacific one.
  expect(pacific.dayOf("2026-08-04T06:02:41Z")).toBe("2026-08-03");
  expect(clockFor("UTC").dayOf("2026-08-04T06:02:41Z")).toBe("2026-08-04");
});

test("an unparseable instant renders as a dash, not Invalid Date", () => {
  const clock = clockFor("UTC");
  for (const draw of [clock.formatDate, clock.formatTime, clock.formatStamp, clock.formatDayTime, clock.dayOf]) {
    expect(draw("not a date")).toBe("—");
  }
});

test("a zone's formatters are built once", () => {
  expect(clockFor("Europe/Berlin")).toBe(clockFor("Europe/Berlin"));
});
