import { isVerifiedZeroRuntime, sandboxRuntimeInterval } from "@/lib/instances";
import type { RequestEvent, Sandbox, Session } from "@/lib/telemetry-types";

// Series derivations for the graph view. Every function here reads the same
// data the tables already receive — one snapshot plus a page of request
// history — and returns plottable numbers. Nothing fetches, nothing samples:
// the host retains no time series, so a chart exists only where the records
// themselves carry enough time information to reconstruct one.
//
// What that allows, and what it does not, is the whole design constraint:
//
//   - Sandbox uptime comes only from measured process intervals. Legacy and
//     interrupted intervals stay unknown, even if startup once succeeded.
//   - Request events are durable (a 100k rolling window in SQLite) and carry
//     received_at, status_code and duration, so rate, mix and latency are
//     honest for whatever page of history the client has loaded.
//   - Sessions are never terminal: state is derived from whether a Sandbox is
//     currently bound, and the transitions are not recorded. Their *starts*
//     can be counted from created_at; their active/idle history cannot be
//     reconstructed at all. No function here pretends otherwise.

export type StatusClass = "2xx" | "3xx" | "4xx" | "5xx";

export const STATUS_CLASSES: StatusClass[] = ["2xx", "3xx", "4xx", "5xx"];

export interface RangeOption {
  id: string;
  label: string;
  durationMS: number;
}

// Ranges are whole hours so bucket boundaries land on readable clock times.
export const RANGES: RangeOption[] = [
  { id: "1h", label: "1h", durationMS: 60 * 60 * 1_000 },
  { id: "6h", label: "6h", durationMS: 6 * 60 * 60 * 1_000 },
  { id: "24h", label: "24h", durationMS: 24 * 60 * 60 * 1_000 },
];

export interface Window {
  start: number;
  end: number;
  bucketMS: number;
  buckets: number;
}

// A window is anchored to the snapshot's observation time rather than to the
// browser clock, so the right edge of every chart is the same instant the
// counters in the header describe.
export function buildWindow(observedAt: string, durationMS: number, buckets = 60): Window {
  const end = Date.parse(observedAt);
  const anchor = Number.isNaN(end) ? Date.now() : end;
  return {
    start: anchor - durationMS,
    end: anchor,
    bucketMS: durationMS / buckets,
    buckets,
  };
}

// Bucket index for an instant, clamped to the window. Callers filter first;
// this only has to be total.
function bucketIndex(at: number, window: Window) {
  const index = Math.floor((at - window.start) / window.bucketMS);
  return Math.min(window.buckets - 1, Math.max(0, index));
}

// The timestamp a bucket is plotted at: its closing edge, so a bar covering
// 09:00–09:06 reads as "as of 09:06" like every other value on the screen.
export function bucketEnd(index: number, window: Window) {
  return window.start + (index + 1) * window.bucketMS;
}

export interface CountPoint {
  at: number;
  value: number;
}

// Unknown intervals cannot contribute fabricated historical concurrency. The
// intervals are exactly the measured ones used by billing and the sandbox table.
export function sandboxConcurrency(sandboxes: Sandbox[], observedAt: string, window: Window): CountPoint[] {
  const intervals = sandboxes
    .map((sandbox) => sandboxRuntimeInterval(sandbox, observedAt))
    .filter((interval): interval is NonNullable<typeof interval> => interval !== undefined);

  return Array.from({ length: window.buckets }, (_unused, index) => {
    const at = bucketEnd(index, window);
    let value = 0;
    for (const interval of intervals) {
      if (interval.start <= at && (interval.terminal ? at < interval.end : at <= interval.end)) value += 1;
    }
    return { at, value };
  });
}

// Sessions created per bucket. Sessions have no terminal state and are never
// deleted, so a "sessions alive" curve would only ever climb; arrivals are the
// part of session history the records can actually answer for.
export function sessionStarts(sessions: Session[], window: Window): CountPoint[] {
  const counts = new Array<number>(window.buckets).fill(0);
  for (const session of sessions) {
    const at = Date.parse(session.created_at);
    if (Number.isNaN(at) || at < window.start || at > window.end) continue;
    counts[bucketIndex(at, window)] += 1;
  }
  return counts.map((value, index) => ({ at: bucketEnd(index, window), value }));
}

export interface StatusBucket {
  at: number;
  total: number;
  counts: Record<StatusClass, number>;
}

function statusClassOf(statusCode: number): StatusClass | null {
  const hundreds = Math.floor(statusCode / 100);
  if (hundreds === 2) return "2xx";
  if (hundreds === 3) return "3xx";
  if (hundreds === 4) return "4xx";
  if (hundreds >= 5) return "5xx";
  return null;
}

// Request volume per bucket, split by status class.
export function requestVolume(events: RequestEvent[], window: Window): StatusBucket[] {
  const buckets: StatusBucket[] = Array.from({ length: window.buckets }, (_unused, index) => ({
    at: bucketEnd(index, window),
    total: 0,
    counts: { "2xx": 0, "3xx": 0, "4xx": 0, "5xx": 0 },
  }));
  for (const event of events) {
    const at = Date.parse(event.received_at);
    if (Number.isNaN(at) || at < window.start || at > window.end) continue;
    const statusClass = statusClassOf(event.status_code);
    if (!statusClass) continue;
    const bucket = buckets[bucketIndex(at, window)];
    bucket.counts[statusClass] += 1;
    bucket.total += 1;
  }
  return buckets;
}

// Nearest-rank percentile over an ascending list. Small samples are the norm
// here — a quiet bucket may hold three requests — so no interpolation.
export function percentile(ascending: number[], fraction: number) {
  if (ascending.length === 0) return undefined;
  const rank = Math.ceil(fraction * ascending.length);
  return ascending[Math.min(ascending.length - 1, Math.max(0, rank - 1))];
}

export interface LatencyBucket {
  at: number;
  count: number;
  p50?: number;
  p95?: number;
  max?: number;
}

export function requestLatency(events: RequestEvent[], window: Window): LatencyBucket[] {
  const samples: number[][] = Array.from({ length: window.buckets }, () => []);
  for (const event of events) {
    const at = Date.parse(event.received_at);
    if (Number.isNaN(at) || at < window.start || at > window.end) continue;
    samples[bucketIndex(at, window)].push(event.duration_ms);
  }
  return samples.map((bucket, index) => {
    bucket.sort((left, right) => left - right);
    return {
      at: bucketEnd(index, window),
      count: bucket.length,
      p50: percentile(bucket, 0.5),
      p95: percentile(bucket, 0.95),
      max: bucket[bucket.length - 1],
    };
  });
}

export interface StartupPoint {
  id: string;
  at: number;
  durationMS: number;
  failed: boolean;
}

// One point per Sandbox that finished booting inside the window, plotted at
// the instant it became ready.
export function startupPoints(sandboxes: Sandbox[], window: Window): StartupPoint[] {
  const points: StartupPoint[] = [];
  for (const sandbox of sandboxes) {
    if (!sandbox.startup) continue;
    const at = Date.parse(sandbox.startup.completed_at);
    if (Number.isNaN(at) || at < window.start || at > window.end) continue;
    points.push({ id: sandbox.id, at, durationMS: sandbox.startup.duration_ms, failed: sandbox.state === "failed" });
  }
  return points.sort((left, right) => left.at - right.at);
}

export interface HistogramBin {
  label: string;
  floorMS: number;
  ceilingMS?: number;
  count: number;
}

// Lifetime bins are decades of minutes: sandbox lifetimes span seconds (a
// failed boot) to hours (a held session), and linear bins put every record in
// the first column.
const LIFETIME_EDGES_MS = [
  0,
  30 * 1_000,
  2 * 60 * 1_000,
  10 * 60 * 1_000,
  30 * 60 * 1_000,
  2 * 60 * 60 * 1_000,
  8 * 60 * 60 * 1_000,
];

const LIFETIME_LABELS = ["<30s", "30s–2m", "2–10m", "10–30m", "30m–2h", "2–8h", "8h+"];

// Only completed measured intervals belong in the lifetime distribution.
export function lifetimeHistogram(sandboxes: Sandbox[]): { bins: HistogramBin[]; sampled: number; live: number; unknown: number } {
  const bins: HistogramBin[] = LIFETIME_EDGES_MS.map((floorMS, index) => ({
    label: LIFETIME_LABELS[index], floorMS, ceilingMS: LIFETIME_EDGES_MS[index + 1], count: 0,
  }));
  let sampled = 0, live = 0, unknown = 0;
  for (const sandbox of sandboxes) {
    if (isVerifiedZeroRuntime(sandbox)) continue;
    const interval = sandboxRuntimeInterval(sandbox, sandbox.runtime_timing?.started_at ?? "");
    if (!interval) { unknown += 1; continue; }
    if (!interval.terminal) { live += 1; continue; }
    const lifetime = interval.end - interval.start;
    let index = 0;
    while (index + 1 < bins.length && lifetime >= (bins[index].ceilingMS ?? Infinity)) index += 1;
    bins[index].count += 1;
    sampled += 1;
  }
  return { bins, sampled, live, unknown };
}

// A "nice" axis maximum: the smallest 1/2/5×10^n step at or above the data,
// so gridlines land on round numbers instead of the exact peak.
export function niceMax(value: number, minimum = 1) {
  const target = Math.max(value, minimum);
  const magnitude = 10 ** Math.floor(Math.log10(target));
  for (const step of [1, 2, 2.5, 5, 10]) {
    if (target <= step * magnitude) return step * magnitude;
  }
  return 10 * magnitude;
}
