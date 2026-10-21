"use client";

import { useMemo, useState } from "react";

import { formatMemory, formatVCPU } from "@/lib/format";
import { niceMax } from "@/lib/graph-series";
import { useClock } from "@/lib/use-clock";
import { useMeasuredWidth } from "@/lib/use-measured-width";
import type { UsageSample } from "@/lib/usage-history";
import type { Sandbox } from "@/lib/telemetry-types";

interface ChartPoint {
  at: number;
  observed_at: string;
  value: number;
}

const PLOT_HEIGHT = 92;
const MARGIN = { top: 10, right: 64, bottom: 20, left: 58 };
const CHART_HEIGHT = MARGIN.top + PLOT_HEIGHT + MARGIN.bottom;
const MIB = 1024 * 1024;

// Axis maximum for a series scaled to its own peak. The hard ceiling only
// joins the plot once that scale would reach it, so idle sandboxes read as
// consumption and busy ones show how close they are to the limit.
function scaleToCeiling(peak: number, minimum: number, ceiling: number) {
  const nice = niceMax(peak, minimum);
  return nice < ceiling ? { max: nice, ceilingVisible: false } : { max: Math.max(ceiling, peak), ceilingVisible: true };
}

function MonitorChart({
  title,
  tone,
  current,
  points,
  yMax,
  ceiling,
  formatValue,
  emptyLabel,
}: {
  title: string;
  tone: "cpu" | "mem";
  current: string;
  points: ChartPoint[];
  yMax: number;
  // Drawn only when the series' scale has grown to reach it.
  ceiling?: { value: number; label: string };
  formatValue: (value: number) => string;
  emptyLabel: string;
}) {
  const { ref, width } = useMeasuredWidth();
  const [hover, setHover] = useState<number | null>(null);
  const { formatTime, formatZone } = useClock();

  const latest = points[points.length - 1];
  const ready = points.length >= 2 && width > MARGIN.left + MARGIN.right + 40;

  const geometry = useMemo(() => {
    if (!ready) return null;
    const innerWidth = width - MARGIN.left - MARGIN.right;
    const t0 = points[0].at;
    const span = Math.max(1, points[points.length - 1].at - t0);
    const x = (at: number) => MARGIN.left + ((at - t0) / span) * innerWidth;
    const y = (value: number) => MARGIN.top + PLOT_HEIGHT - (Math.min(value, yMax) / yMax) * PLOT_HEIGHT;
    const coords = points.map((point) => ({ px: x(point.at), py: y(point.value) }));
    const line = coords.map(({ px, py }, i) => `${i === 0 ? "M" : "L"}${px.toFixed(1)} ${py.toFixed(1)}`).join(" ");
    const baseline = MARGIN.top + PLOT_HEIGHT;
    const area = `${line} L${coords[coords.length - 1].px.toFixed(1)} ${baseline} L${coords[0].px.toFixed(1)} ${baseline} Z`;
    return { coords, line, area, baseline };
  }, [points, ready, width, yMax]);

  const moveTo = (clientX: number, rect: DOMRect) => {
    if (!geometry) return;
    const px = clientX - rect.left;
    let nearest = 0;
    for (let i = 1; i < geometry.coords.length; i += 1) {
      if (Math.abs(geometry.coords[i].px - px) < Math.abs(geometry.coords[nearest].px - px)) nearest = i;
    }
    setHover(nearest);
  };

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (!geometry) return;
    const last = points.length - 1;
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

  const hovered = hover !== null ? points[hover] : null;
  const hoveredCoord = hover !== null ? geometry?.coords[hover] : null;
  const tooltipOnLeft = hoveredCoord !== null && hoveredCoord !== undefined && hoveredCoord.px > width / 2;
  const ticks = [0, 0.5, 1];

  return (
    <div className={`monitor-chart tone-${tone}`}>
      <div className="chart-head">
        <h3>{title}</h3>
        <span className="chart-current">{current}</span>
      </div>
      <div
        className="chart-plot"
        ref={ref}
        tabIndex={ready ? 0 : -1}
        role="img"
        aria-label={latest ? `${title}: latest reading ${formatValue(latest.value)} at ${formatTime(latest.observed_at)} ${formatZone(latest.observed_at)}` : `${title}: no samples yet`}
        onKeyDown={onKeyDown}
        onBlur={() => setHover(null)}
      >
        {!ready ? (
          <div className="chart-empty">{emptyLabel}</div>
        ) : (
          <>
            <svg
              width={width}
              height={CHART_HEIGHT}
              onPointerMove={(event) => moveTo(event.clientX, event.currentTarget.getBoundingClientRect())}
              onPointerLeave={() => setHover(null)}
            >
              {ticks.map((tick) => {
                const y = MARGIN.top + PLOT_HEIGHT - tick * PLOT_HEIGHT;
                return (
                  <g key={tick}>
                    <line className="grid-line" x1={MARGIN.left} x2={width - MARGIN.right} y1={y} y2={y} />
                    <text className="tick-label" x={MARGIN.left - 8} y={y + 3} textAnchor="end">
                      {formatValue(tick * yMax)}
                    </text>
                  </g>
                );
              })}
              <text className="tick-label" x={MARGIN.left} y={CHART_HEIGHT - 6} textAnchor="start">
                {formatTime(points[0].observed_at)}
              </text>
              <text className="tick-label" x={width - MARGIN.right} y={CHART_HEIGHT - 6} textAnchor="end">
                {formatTime(points[points.length - 1].observed_at)}
              </text>
              {ceiling && (() => {
                const y = MARGIN.top + PLOT_HEIGHT - (ceiling.value / yMax) * PLOT_HEIGHT;
                return (
                  <g>
                    <line className="reference-line" x1={MARGIN.left} x2={width - MARGIN.right} y1={y} y2={y} />
                    <text className="tick-label" x={MARGIN.left + 4} y={y + 11} textAnchor="start">
                      {ceiling.label}
                    </text>
                  </g>
                );
              })()}
              <path className="series-area" d={geometry!.area} />
              <path className="series-line" d={geometry!.line} />
              {hoveredCoord && (
                <line className="crosshair" x1={hoveredCoord.px} x2={hoveredCoord.px} y1={MARGIN.top} y2={geometry!.baseline} />
              )}
              <circle
                className="series-dot"
                cx={geometry!.coords[points.length - 1].px}
                cy={geometry!.coords[points.length - 1].py}
                r={3}
              />
              {hoveredCoord && <circle className="series-dot" cx={hoveredCoord.px} cy={hoveredCoord.py} r={3.5} />}
              <text
                className="end-label"
                x={geometry!.coords[points.length - 1].px + 8}
                y={Math.max(MARGIN.top + 8, geometry!.coords[points.length - 1].py + 3)}
              >
                {formatValue(points[points.length - 1].value)}
              </text>
            </svg>
            {hovered && hoveredCoord && (
              <div
                className={`chart-tooltip${tooltipOnLeft ? " flip" : ""}`}
                style={{ left: `${hoveredCoord.px}px`, top: `${MARGIN.top}px` }}
              >
                <i aria-hidden="true" />
                <strong>{formatValue(hovered.value)}</strong>
                <small>{formatTime(hovered.observed_at)} {formatZone(hovered.observed_at)}</small>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

export function SandboxMonitor({
  instance,
  samples,
  live,
  showLimits = true,
  onClose,
}: {
  instance: Sandbox;
  samples: UsageSample[];
  live: boolean;
  // The operator screen draws the vcpu and memory ceilings; a customer's
  // view charts consumption alone, scaled to its own peak.
  showLimits?: boolean;
  onClose: () => void;
}) {
  // CPU needs two consecutive cgroup readings, so the first sample of a
  // sandbox has memory only; the charts plot whatever is defined. Both axes
  // scale to observed consumption, not to the sandbox's shape.
  const cpuPoints: ChartPoint[] = samples
    .filter((sample) => sample.cpu_percent !== undefined)
    .map((sample) => ({
      at: Date.parse(sample.observed_at),
      observed_at: sample.observed_at,
      value: (sample.cpu_percent! / 100) * instance.vcpus,
    }));
  const memoryPoints: ChartPoint[] = samples.filter((sample) => sample.memory_bytes !== undefined).map((sample) => ({
    at: Date.parse(sample.observed_at),
    observed_at: sample.observed_at,
    value: sample.memory_bytes!,
  }));
  const cpuPeak = Math.max(0, ...cpuPoints.map((point) => point.value));
  const memoryPeak = Math.max(0, ...memoryPoints.map((point) => point.value));
  // Hard ceilings: the vcpu count, and the elastic max (boot size when the
  // sandbox has none). Memory scales in mib so axis steps land on round sizes.
  const memoryLimit = (instance.memory_max_mib ?? instance.memory_mib) * MIB;
  const cpuScale = showLimits
    ? scaleToCeiling(cpuPeak, 0.1, instance.vcpus)
    : { max: niceMax(cpuPeak, 0.1), ceilingVisible: false };
  const memoryScale = showLimits
    ? scaleToCeiling(memoryPeak / MIB, 64, memoryLimit / MIB)
    : { max: niceMax(memoryPeak / MIB, 64), ceilingVisible: false };
  const latestCPU = cpuPoints[cpuPoints.length - 1];
  const latestMemoryValue = samples[samples.length - 1]?.memory_bytes;
  const recentFirst = [...samples].reverse();
  const { formatDate } = useClock();

  return (
    <section className="monitor" aria-label={`Live monitor for Sandbox ${instance.id}`}>
      {/* Build, startup and the rest of the record sit in the selected row
          below; the head only names the sandbox being charted. */}
      <div className="monitor-head">
        <strong>{instance.id}</strong>
        <dl className="monitor-meta">
          <dt>State</dt>
          <dd className={`st-${instance.state}`} title={instance.failure?.reason}>{instance.state}</dd>
        </dl>
        <button className="monitor-close" type="button" onClick={onClose}>Close</button>
      </div>

      {samples.length === 0 && !live ? (
        <p className="empty-note">No usage was recorded while this sandbox ran</p>
      ) : (
        <div className="monitor-charts">
          <MonitorChart
            title="CPU"
            tone="cpu"
            current={latestCPU ? formatVCPU(latestCPU.value) : "No samples"}
            points={cpuPoints}
            yMax={cpuScale.max}
            ceiling={cpuScale.ceilingVisible ? { value: instance.vcpus, label: `${instance.vcpus} vcpu limit` } : undefined}
            formatValue={formatVCPU}
            emptyLabel={live ? "Collecting CPU samples — the first rate needs two readings…" : "No CPU samples for this sandbox"}
          />
          <MonitorChart
            title="Memory"
            tone="mem"
            current={latestMemoryValue === undefined ? "Unavailable" : formatMemory(latestMemoryValue)}
            points={memoryPoints}
            yMax={memoryScale.max * MIB}
            ceiling={memoryScale.ceilingVisible ? { value: memoryLimit, label: `${formatMemory(memoryLimit)} limit` } : undefined}
            formatValue={(value) => (value === 0 ? "0 mib" : formatMemory(value))}
            emptyLabel={live ? "Waiting for sandbox memory measurements…" : "No memory samples for this sandbox"}
          />
        </div>
      )}

      {recentFirst.length > 0 && (
        <details className="monitor-samples">
          <summary>View sample table ({samples.length})</summary>
          <table className="data-table">
            <colgroup>
              <col />
              <col className="c-num" />
              <col className="c-count" />
            </colgroup>
            <thead>
              <tr>
                <th>Observed</th>
                <th className="num">CPU</th>
                <th className="num">Memory</th>
              </tr>
            </thead>
            <tbody>
              {recentFirst.map((sample) => (
                <tr key={sample.observed_at}>
                  <td className="sub">{formatDate(sample.observed_at)}</td>
                  <td className="num sub">{sample.cpu_percent === undefined ? "—" : formatVCPU((sample.cpu_percent / 100) * instance.vcpus)}</td>
                  <td className="num sub">{sample.memory_bytes === undefined ? "—" : formatMemory(sample.memory_bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </section>
  );
}
