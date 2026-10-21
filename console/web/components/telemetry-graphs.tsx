"use client";

import { useEffect, useMemo, useState } from "react";

import { formatDuration, type Clock } from "@/lib/format";
import {
  buildWindow,
  lifetimeHistogram,
  niceMax,
  percentile,
  RANGES,
  requestLatency,
  requestVolume,
  sandboxConcurrency,
  sessionStarts,
  startupPoints,
  STATUS_CLASSES,
  type CountPoint,
  type Window,
} from "@/lib/graph-series";
import { useClock } from "@/lib/use-clock";
import { useMeasuredWidth } from "@/lib/use-measured-width";
import type { RequestEvent, Sandbox, Snapshot } from "@/lib/telemetry-types";

// The graph view answers "how has this App behaved" from the same two
// payloads the tables read. It replaces scanning three growing lists with six
// shapes; the lists stay reachable for the row-level detail a shape can't
// carry (which sandbox, which build, which request).

const PLOT_HEIGHT = 168;
const MARGIN = { top: 12, right: 14, bottom: 20, left: 48 };
const CHART_HEIGHT = MARGIN.top + PLOT_HEIGHT + MARGIN.bottom;
const MIN_PLOT_WIDTH = MARGIN.left + MARGIN.right + 48;

// A window that crosses midnight needs the day on its axis labels, or a 24h
// range reads "09:41:52" at both ends. Midnight is the viewer's own, so the
// clock decides which side of it each end falls on.
function formatEdge(at: number, window: Window, clock: Clock) {
  const iso = new Date(at).toISOString();
  const crossesDay = clock.dayOf(new Date(window.start).toISOString()) !== clock.dayOf(new Date(window.end).toISOString());
  return crossesDay ? clock.formatDayTime(iso) : clock.formatTime(iso);
}

interface Scales {
  x: (at: number) => number;
  y: (value: number) => number;
  band: number;
  baseline: number;
  right: number;
}

// ---- Chart frame ----------------------------------------------------------

// Every time chart shares one frame: the same x domain (the selected window),
// the same three gridlines, the same axis labels, and one nearest-bucket
// hover. Series draw themselves through the scales; the tooltip body is the
// panel's own, since a count, a status mix and a latency pair read differently.
function TimeChart({
  window,
  yTicks,
  formatY,
  logScale = false,
  label,
  series,
  tooltip,
}: {
  window: Window;
  // Actual values, ascending, the last of which is the axis maximum. Ticks are
  // values rather than fractions so a gridline never sits at 12.5 under a
  // label that reads 13.
  yTicks: number[];
  formatY: (value: number) => string;
  // Latency spans three orders of magnitude — a p50 of 40ms beside a p95 of
  // 8s — so it is the one axis that has to be logarithmic to show both.
  logScale?: boolean;
  label: string;
  series: (scales: Scales) => React.ReactNode;
  tooltip: (index: number) => { body: React.ReactNode; at: number } | null;
}) {
  const { ref, width } = useMeasuredWidth();
  const [hover, setHover] = useState<number | null>(null);
  const clock = useClock();
  const ready = width > MIN_PLOT_WIDTH;
  const yMin = yTicks[0];
  const yMax = yTicks[yTicks.length - 1];

  const scales = useMemo<Scales | null>(() => {
    if (!ready) return null;
    const innerWidth = width - MARGIN.left - MARGIN.right;
    const span = Math.max(1, window.end - window.start);
    const floor = logScale ? Math.log10(Math.max(yMin, 1)) : yMin;
    const ceiling = logScale ? Math.log10(Math.max(yMax, 10)) : yMax;
    const project = (value: number) => (logScale ? Math.log10(Math.max(value, Math.max(yMin, 1))) : value);
    return {
      x: (at: number) => MARGIN.left + ((at - window.start) / span) * innerWidth,
      y: (value: number) =>
        MARGIN.top + PLOT_HEIGHT - ((Math.min(project(value), ceiling) - floor) / (ceiling - floor)) * PLOT_HEIGHT,
      band: innerWidth / window.buckets,
      baseline: MARGIN.top + PLOT_HEIGHT,
      right: width - MARGIN.right,
    };
  }, [logScale, ready, width, window, yMax, yMin]);

  const bucketAt = (clientX: number, rect: DOMRect) => {
    if (!scales) return;
    const offset = clientX - rect.left - MARGIN.left;
    const index = Math.floor(offset / scales.band);
    setHover(index < 0 || index >= window.buckets ? null : index);
  };

  const onKeyDown = (event: React.KeyboardEvent) => {
    const last = window.buckets - 1;
    const current = hover ?? last;
    let next: number | null = null;
    if (event.key === "ArrowLeft") next = Math.max(0, current - 1);
    else if (event.key === "ArrowRight") next = Math.min(last, current + 1);
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = last;
    else if (event.key === "Escape") {
      setHover(null);
      return;
    } else return;
    event.preventDefault();
    setHover(next);
  };

  const hovered = hover === null ? null : tooltip(hover);
  const hoverX = scales && hover !== null ? scales.x(window.start + (hover + 0.5) * window.bucketMS) : null;
  const flip = hoverX !== null && hoverX > width / 2;

  return (
    <div
      className="chart-plot"
      ref={ref}
      tabIndex={ready ? 0 : -1}
      role="img"
      aria-label={label}
      onKeyDown={onKeyDown}
      onBlur={() => setHover(null)}
    >
      {!ready || !scales ? (
        <div className="chart-empty">Measuring…</div>
      ) : (
        <>
          <svg
            width={width}
            height={CHART_HEIGHT}
            onPointerMove={(event) => bucketAt(event.clientX, event.currentTarget.getBoundingClientRect())}
            onPointerLeave={() => setHover(null)}
          >
            {yTicks.map((tick) => {
              const y = scales.y(tick);
              return (
                <g key={tick}>
                  <line className="grid-line" x1={MARGIN.left} x2={scales.right} y1={y} y2={y} />
                  <text className="tick-label" x={MARGIN.left - 6} y={y + 3} textAnchor="end">
                    {formatY(tick)}
                  </text>
                </g>
              );
            })}
            {series(scales)}
            {hoverX !== null && (
              <line className="crosshair" x1={hoverX} x2={hoverX} y1={MARGIN.top} y2={scales.baseline} />
            )}
            <text className="tick-label" x={MARGIN.left} y={CHART_HEIGHT - 5} textAnchor="start">
              {formatEdge(window.start, window, clock)}
            </text>
            <text className="tick-label" x={scales.right} y={CHART_HEIGHT - 5} textAnchor="end">
              {formatEdge(window.end, window, clock)}
            </text>
          </svg>
          {hovered && hoverX !== null && (
            <div className={`chart-tooltip${flip ? " flip" : ""}`} style={{ left: `${hoverX}px`, top: `${MARGIN.top}px` }}>
              <span className="tip-body">{hovered.body}</span>
              <small>{formatEdge(hovered.at, window, clock)}</small>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function GraphPanel({ title, current, children }: { title: string; current: string; children: React.ReactNode }) {
  return (
    <section className="graph-panel">
      <div className="chart-head">
        <h3>{title}</h3>
        <span className="chart-current">{current}</span>
      </div>
      {children}
    </section>
  );
}

// ---- Series shapes --------------------------------------------------------

// Counts are step functions: a concurrency of 3 held for six minutes is a
// plateau, not a ramp between two readings.
function stepPath(points: CountPoint[], scales: Scales, bucketMS: number) {
  const segments: string[] = [];
  points.forEach((point, index) => {
    const left = scales.x(point.at - bucketMS);
    const right = scales.x(point.at);
    const y = scales.y(point.value);
    segments.push(`${index === 0 ? "M" : "L"}${left.toFixed(1)} ${y.toFixed(1)}`, `L${right.toFixed(1)} ${y.toFixed(1)}`);
  });
  return segments.join(" ");
}

function StepArea({ points, scales, bucketMS }: { points: CountPoint[]; scales: Scales; bucketMS: number }) {
  if (points.length === 0) return null;
  const line = stepPath(points, scales, bucketMS);
  const first = scales.x(points[0].at - bucketMS);
  const last = scales.x(points[points.length - 1].at);
  return (
    <>
      <path className="series-area" d={`${line} L${last.toFixed(1)} ${scales.baseline} L${first.toFixed(1)} ${scales.baseline} Z`} />
      <path className="series-line" d={line} />
    </>
  );
}

// Bars leave a hairline gap so adjacent buckets stay countable, and never
// render narrower than a pixel at 24h.
function barGeometry(scales: Scales, at: number, bucketMS: number) {
  const left = scales.x(at - bucketMS);
  const width = Math.max(1, scales.x(at) - left - 1);
  return { left, width };
}

// A step line broken into one path per run of populated buckets, so a quiet
// stretch reads as no data rather than as a straight line between two peaks.
function gappedStepPaths(buckets: Array<{ at: number; value?: number }>, scales: Scales, bucketMS: number) {
  const paths: string[] = [];
  let run: string[] = [];
  for (const bucket of buckets) {
    if (bucket.value === undefined) {
      if (run.length > 0) paths.push(run.join(" "));
      run = [];
      continue;
    }
    const y = scales.y(bucket.value).toFixed(1);
    run.push(`${run.length === 0 ? "M" : "L"}${scales.x(bucket.at - bucketMS).toFixed(1)} ${y}`, `L${scales.x(bucket.at).toFixed(1)} ${y}`);
  }
  if (run.length > 0) paths.push(run.join(" "));
  return paths;
}

// Count axes are labelled at their halfway point, so the maximum has to halve
// into a whole number — a gridline at 12.5 under a label reading 13 is the
// bug this avoids.
function countTicks(max: number) {
  const top = max % 2 === 0 ? max : max + 1;
  return [0, top / 2, top];
}

// Latency decades spanning the data: starting the axis at 1ms when the
// fastest bucket is 40ms would spend a third of the plot on empty decades.
function latencyTicks(min: number, max: number) {
  const floor = 10 ** Math.floor(Math.log10(Math.max(min, 1)));
  const ticks = [floor];
  while (ticks[ticks.length - 1] < max) ticks.push(ticks[ticks.length - 1] * 10);
  return ticks;
}

export function SandboxStartupGraph({
  sandboxes,
  observedAt,
  durationMS,
  rangeLabel,
}: {
  sandboxes: Sandbox[];
  observedAt: string;
  durationMS: number;
  rangeLabel: string;
}) {
  const window = buildWindow(observedAt, durationMS);
  const startups = startupPoints(sandboxes, window);
  const startupMax = niceMax(Math.max(0, ...startups.map(({ durationMS: duration }) => duration)), 1_000);
  const durations = startups.map(({ durationMS: duration }) => duration).sort((a, b) => a - b);
  const startupP50 = percentile(durations, 0.5);
  const startupP95 = percentile(durations, 0.95);
  return (
    <GraphPanel
      title="Sandbox startup time"
      current={startups.length === 0 ? "No boots in range" : `p50 ${formatDuration(startupP50)} · p95 ${formatDuration(startupP95)}`}
    >
      <TimeChart
        window={window}
        yTicks={[0, startupMax / 2, startupMax]}
        formatY={(value) => formatDuration(value)}
        label={`Sandbox startup duration over the last ${rangeLabel}`}
        series={(scales) => (
          <>
            {startupP50 !== undefined && (
              <line className="reference-line" x1={MARGIN.left} x2={scales.right} y1={scales.y(startupP50)} y2={scales.y(startupP50)} />
            )}
            {startups.map((point) => (
              <circle
                key={point.id}
                className={`scatter-dot${point.failed ? " failed" : ""}`}
                cx={scales.x(point.at)}
                cy={scales.y(point.durationMS)}
                r={2.5}
              />
            ))}
          </>
        )}
        tooltip={(index) => {
          const from = window.start + index * window.bucketMS;
          const inBucket = startups.filter((point) => point.at >= from && point.at < from + window.bucketMS);
          if (inBucket.length === 0) return null;
          const slowest = inBucket.reduce((worst, point) => (point.durationMS > worst.durationMS ? point : worst));
          return {
            at: from + window.bucketMS,
            body: (
              <>
                <strong>{formatDuration(slowest.durationMS)}</strong>
                <em className="dim">{inBucket.length === 1 ? slowest.id : `slowest of ${inBucket.length}`}</em>
              </>
            ),
          };
        }}
      />
    </GraphPanel>
  );
}

// ---- View -----------------------------------------------------------------

export function TelemetryGraphs({
  snapshot,
  events,
  historyComplete,
  onWindowChange,
}: {
  snapshot: Snapshot;
  events: RequestEvent[];
  // The loaded history is a bounded slice of a durable log. When older events
  // are still missing, request counts are lower bounds and are marked as such
  // rather than implying the traffic stopped.
  historyComplete: boolean;
  // Announces the window the charts need covered, so the caller can backfill.
  onWindowChange?: (start: number) => void;
}) {
  const [rangeId, setRangeId] = useState("6h");
  const range = RANGES.find(({ id }) => id === rangeId) ?? RANGES[1];
  const window = useMemo(() => buildWindow(snapshot.observed_at, range.durationMS), [snapshot.observed_at, range.durationMS]);

  useEffect(() => {
    onWindowChange?.(window.start);
  }, [onWindowChange, window.start]);

  const concurrency = useMemo(() => sandboxConcurrency(snapshot.sandboxes, snapshot.observed_at, window), [snapshot, window]);
  const starts = useMemo(() => sessionStarts(snapshot.sessions, window), [snapshot.sessions, window]);
  const volume = useMemo(() => requestVolume(events, window), [events, window]);
  const latency = useMemo(() => requestLatency(events, window), [events, window]);
  const lifetimes = useMemo(() => lifetimeHistogram(snapshot.sandboxes), [snapshot.sandboxes]);

  const concurrencyMax = niceMax(Math.max(...concurrency.map(({ value }) => value)), 2);
  const startsMax = niceMax(Math.max(...starts.map(({ value }) => value)), 2);
  const volumeMax = niceMax(Math.max(...volume.map(({ total }) => total)), 4);
  const latencyValues = latency.flatMap(({ p50, p95 }) => (p50 === undefined || p95 === undefined ? [] : [p50, p95]));
  const latencyMax = niceMax(Math.max(0, ...latencyValues), 100);
  const latencyMin = latencyValues.length === 0 ? 1 : Math.min(...latencyValues);

  const windowedEvents = events.filter((event) => {
    const at = Date.parse(event.received_at);
    return at >= window.start && at <= window.end;
  });
  const errors = windowedEvents.filter((event) => event.status_code >= 500).length;
  const errorRate = windowedEvents.length === 0 ? 0 : (errors / windowedEvents.length) * 100;
  const liveSandboxes = concurrency[concurrency.length - 1]?.value ?? 0;
  const peakSandboxes = Math.max(...concurrency.map(({ value }) => value));
  const latencySamples = windowedEvents.map(({ duration_ms }) => duration_ms).sort((a, b) => a - b);
  const overallP95 = percentile(latencySamples, 0.95);
  const histogramMax = Math.max(1, ...lifetimes.bins.map(({ count }) => count));

  return (
    <div className="graph-view">
      <div className="graph-controls">
        <div className="range-picker" role="group" aria-label="Time range">
          {RANGES.map((option) => (
            <button
              key={option.id}
              type="button"
              className="tab-chip"
              aria-pressed={option.id === range.id}
              onClick={() => setRangeId(option.id)}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>

      <div className="graph-grid">
        <GraphPanel
          title="Sandboxes alive"
          current={`${liveSandboxes} now · ${peakSandboxes} peak${lifetimes.unknown ? ` · ${lifetimes.unknown} unknown` : ""}`}
        >
          <TimeChart
            window={window}
            yTicks={countTicks(concurrencyMax)}
            formatY={(value) => String(Math.round(value))}
            label={`Sandboxes alive over the last ${range.label}: ${liveSandboxes} now, ${peakSandboxes} at peak`}
            series={(scales) => <StepArea points={concurrency} scales={scales} bucketMS={window.bucketMS} />}
            tooltip={(index) => {
              const point = concurrency[index];
              if (!point) return null;
              return { at: point.at, body: <strong>{point.value} alive</strong> };
            }}
          />
        </GraphPanel>

        <GraphPanel
          title="Requests · status mix"
          current={`${historyComplete ? "" : "≥"}${windowedEvents.length} in ${range.label} · ${errorRate.toFixed(errorRate < 10 ? 1 : 0)}% 5xx`}
        >
          <TimeChart
            window={window}
            yTicks={countTicks(volumeMax)}
            formatY={(value) => String(Math.round(value))}
            label={`Requests per bucket by status class over the last ${range.label}`}
            series={(scales) =>
              volume.map((bucket) => {
                if (bucket.total === 0) return null;
                const { left, width } = barGeometry(scales, bucket.at, window.bucketMS);
                let stacked = 0;
                return (
                  <g key={bucket.at}>
                    {STATUS_CLASSES.map((statusClass) => {
                      const count = bucket.counts[statusClass];
                      if (count === 0) return null;
                      const top = scales.y(stacked + count);
                      const height = scales.y(stacked) - top;
                      stacked += count;
                      return <rect key={statusClass} className={`bar seg-${statusClass}`} x={left} y={top} width={width} height={height} />;
                    })}
                  </g>
                );
              })
            }
            tooltip={(index) => {
              const bucket = volume[index];
              if (!bucket || bucket.total === 0) return null;
              return {
                at: bucket.at,
                body: (
                  <>
                    <strong>{bucket.total}</strong>
                    {STATUS_CLASSES.filter((statusClass) => bucket.counts[statusClass] > 0).map((statusClass) => (
                      <em key={statusClass} className={`http-${statusClass}`}>
                        {bucket.counts[statusClass]} {statusClass}
                      </em>
                    ))}
                  </>
                ),
              };
            }}
          />
          <ul className="graph-legend">
            {STATUS_CLASSES.map((statusClass) => (
              <li key={statusClass}>
                <span className={`swatch seg-${statusClass}`} aria-hidden="true" />
                {statusClass}
              </li>
            ))}
          </ul>
        </GraphPanel>

        <GraphPanel
          title="Request latency"
          current={overallP95 === undefined ? "No requests" : `p95 ${formatDuration(overallP95)}`}
        >
          <TimeChart
            window={window}
            yTicks={latencyTicks(latencyMin, latencyMax)}
            formatY={(value) => formatDuration(value)}
            logScale
            label={`Request latency p50 and p95 over the last ${range.label}`}
            series={(scales) => (
              <>
                {gappedStepPaths(latency.map(({ at, p95 }) => ({ at, value: p95 })), scales, window.bucketMS).map((path) => (
                  <path key={path} className="line-p95" d={path} />
                ))}
                {gappedStepPaths(latency.map(({ at, p50 }) => ({ at, value: p50 })), scales, window.bucketMS).map((path) => (
                  <path key={path} className="line-p50" d={path} />
                ))}
              </>
            )}
            tooltip={(index) => {
              const bucket = latency[index];
              if (!bucket || bucket.count === 0) return null;
              return {
                at: bucket.at,
                body: (
                  <>
                    <strong>p50 {formatDuration(bucket.p50)}</strong>
                    <em>p95 {formatDuration(bucket.p95)}</em>
                    <em className="dim">{bucket.count} req</em>
                  </>
                ),
              };
            }}
          />
          <ul className="graph-legend">
            <li><span className="swatch line-p50" aria-hidden="true" />p50</li>
            <li><span className="swatch line-p95" aria-hidden="true" />p95</li>
          </ul>
        </GraphPanel>

        <SandboxStartupGraph
          sandboxes={snapshot.sandboxes}
          observedAt={snapshot.observed_at}
          durationMS={range.durationMS}
          rangeLabel={range.label}
        />

        <GraphPanel
          title="Session starts"
          current={`${starts.reduce((total, { value }) => total + value, 0)} in ${range.label} · ${snapshot.sessions.length} held`}
        >
          <TimeChart
            window={window}
            yTicks={countTicks(startsMax)}
            formatY={(value) => String(Math.round(value))}
            label={`Sessions created per bucket over the last ${range.label}`}
            series={(scales) =>
              starts.map((point) => {
                if (point.value === 0) return null;
                const { left, width } = barGeometry(scales, point.at, window.bucketMS);
                const top = scales.y(point.value);
                return <rect key={point.at} className="bar seg-session" x={left} y={top} width={width} height={scales.baseline - top} />;
              })
            }
            tooltip={(index) => {
              const point = starts[index];
              if (!point || point.value === 0) return null;
              return { at: point.at, body: <strong>{point.value} started</strong> };
            }}
          />
        </GraphPanel>

        <GraphPanel
          title="Sandbox uptime"
          current={`${lifetimes.sampled} completed · ${lifetimes.live} still alive${lifetimes.unknown ? ` · ${lifetimes.unknown} unknown` : ""}`}
        >
          <div className="histogram" role="img" aria-label={`Sandbox uptime distribution over ${lifetimes.sampled} completed records`}>
            {lifetimes.bins.map((bin) => (
              <div className="hist-bin" key={bin.label}>
                <div className="hist-track">
                  <div
                    className={`hist-bar${bin.count === 0 ? " zero" : ""}`}
                    style={{ height: `${(bin.count / histogramMax) * 100}%` }}
                  />
                </div>
                <b className={bin.count === 0 ? "zero" : undefined}>{bin.count}</b>
                <span>{bin.label}</span>
              </div>
            ))}
          </div>
        </GraphPanel>
      </div>
    </div>
  );
}
