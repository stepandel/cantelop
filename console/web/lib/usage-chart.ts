import type { UsageSample } from "@/lib/usage-history";

export interface UsageChartPoint {
  at: number;
  observed_at: string;
  value?: number;
}

export interface UsageChartRange {
  start: string;
  end: string;
}

// Missing observations keep their timestamps. Both metrics use the full
// observation window, so an outage leaves space rather than stretching the
// last good reading to the present or connecting across missing samples.
export function buildUsageSeries(samples: UsageSample[], vcpus: number) {
  const points = (value: (sample: UsageSample) => number | undefined): UsageChartPoint[] => samples.map((sample) => ({
    at: Date.parse(sample.observed_at),
    observed_at: sample.observed_at,
    value: value(sample),
  }));
  const range: UsageChartRange | undefined = samples.length ? {
    start: samples[0].observed_at,
    end: samples[samples.length - 1].observed_at,
  } : undefined;
  return {
    range,
    cpu: points((sample) => sample.cpu_percent === undefined ? undefined : (sample.cpu_percent / 100) * vcpus),
    memory: points((sample) => sample.memory_bytes),
  };
}

// Usage arrives on a 5-second poll. A wider interval than two polls means no
// sample was recorded, so it breaks the line like a missing reading does.
const MAX_SAMPLE_GAP_MS = 2.5 * 5_000;

interface PlotPoint {
  px: number;
  py: number;
}

export function usageChartGeometry(
  points: UsageChartPoint[],
  range: UsageChartRange,
  plot: { left: number; top: number; width: number; height: number },
  yMax: number,
) {
  const start = Date.parse(range.start);
  const span = Math.max(1, Date.parse(range.end) - start);
  const baseline = plot.top + plot.height;
  const coords = points.map((point) => ({
    px: plot.left + ((point.at - start) / span) * plot.width,
    py: point.value === undefined ? undefined : baseline - (Math.min(point.value, yMax) / yMax) * plot.height,
  }));
  const segments: { coords: PlotPoint[]; line: string; area: string }[] = [];
  let run: PlotPoint[] = [];
  const finishRun = () => {
    if (!run.length) return;
    const line = run.map(({ px, py }, i) => `${i === 0 ? "M" : "L"}${px.toFixed(1)} ${py.toFixed(1)}`).join(" ");
    const area = `${line} L${run[run.length - 1].px.toFixed(1)} ${baseline} L${run[0].px.toFixed(1)} ${baseline} Z`;
    segments.push({ coords: run, line, area });
    run = [];
  };
  coords.forEach((coord, i) => {
    if (i > 0 && points[i].at - points[i - 1].at > MAX_SAMPLE_GAP_MS) finishRun();
    if (coord.py === undefined) finishRun();
    else run.push({ px: coord.px, py: coord.py });
  });
  finishRun();
  return { coords, segments, baseline };
}
