import { expect, test } from "bun:test";

import { buildUsageSeries, usageChartGeometry } from "../lib/usage-chart.ts";

const at = (seconds) => new Date(Date.UTC(2026, 9, 22, 12, 0, seconds)).toISOString();
const plot = { left: 0, top: 0, width: 100, height: 100 };

test("memory outage leaves a blank tail on the same timeline as continuing CPU readings", () => {
  const series = buildUsageSeries([
    { observed_at: at(0), memory_bytes: 480 },
    { observed_at: at(1), memory_bytes: 500, cpu_percent: 0 },
    { observed_at: at(2), cpu_percent: 0 },
    { observed_at: at(10), cpu_percent: 0 },
  ], 4);
  expect(series.range).toEqual({ start: at(0), end: at(10) });
  const cpu = usageChartGeometry(series.cpu, series.range, plot, 4);
  const memory = usageChartGeometry(series.memory, series.range, plot, 1000);
  expect(memory.segments).toHaveLength(1);
  expect(memory.segments[0].coords.map((point) => point.px)).toEqual([0, 10]);
  expect(memory.coords[3]).toEqual({ px: 100, py: undefined });
  expect(cpu.coords[3]).toEqual({ px: 100, py: 100 });
  expect(series.memory[3].value).toBeUndefined();
  expect(series.cpu[3].value).toBe(0);
});

test("recovered memory readings start a new line and filled area after a missing sample", () => {
  const series = buildUsageSeries([
    { observed_at: at(0), memory_bytes: 10 },
    { observed_at: at(5), memory_bytes: 20 },
    { observed_at: at(10) },
    { observed_at: at(15), memory_bytes: 30 },
    { observed_at: at(20), memory_bytes: 40 },
  ], 4);
  const geometry = usageChartGeometry(series.memory, series.range, plot, 100);
  expect(geometry.segments.map((segment) => segment.line)).toEqual([
    "M0.0 90.0 L25.0 80.0", "M75.0 70.0 L100.0 60.0",
  ]);
  expect(geometry.segments[0].area).toEndWith("L25.0 100 L0.0 100 Z");
  expect(geometry.segments[1].area).toEndWith("L100.0 100 L75.0 100 Z");
  expect(geometry.coords[2].py).toBeUndefined();
});

test("an interval with no recorded samples breaks the line", () => {
  const series = buildUsageSeries([
    { observed_at: at(0), memory_bytes: 10 },
    { observed_at: at(5), memory_bytes: 20 },
    { observed_at: at(40), memory_bytes: 30 },
    { observed_at: at(45), memory_bytes: 40 },
  ], 4);
  const geometry = usageChartGeometry(series.memory, series.range, plot, 100);
  expect(geometry.segments.map((segment) => segment.coords.length)).toEqual([2, 2]);
});

test("isolated zero readings stay visible while absent readings create no line", () => {
  const series = buildUsageSeries([
    { observed_at: at(0) },
    { observed_at: at(5), memory_bytes: 0 },
    { observed_at: at(10) },
  ], 4);
  const memory = usageChartGeometry(series.memory, series.range, plot, 1);
  expect(memory.segments).toHaveLength(1);
  expect(memory.segments[0].coords).toEqual([{ px: 50, py: 100 }]);
  expect(usageChartGeometry(series.cpu, series.range, plot, 4).segments).toEqual([]);
  expect(buildUsageSeries([], 4)).toEqual({ range: undefined, cpu: [], memory: [] });
});
