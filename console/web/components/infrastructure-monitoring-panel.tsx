"use client";

import { useCallback, useMemo, useState } from "react";

import { PlatformDiagnosticsPanel } from "@/components/platform-diagnostics-panel";
import { SandboxStartupGraph } from "@/components/telemetry-graphs";
import {
  HEALTH_CHART_ID,
  HOST_RESOURCE_CHART_IDS,
  hostStatus,
  infrastructureHosts,
  latestValues,
  orderedPoints,
  serviceStatuses,
} from "@/lib/infrastructure-hosts";
import type {
  InfrastructureChart,
  InfrastructureHost,
  InfrastructurePoint,
  InfrastructureRange,
  InfrastructureSnapshot,
} from "@/lib/infrastructure-types";
import { useClock } from "@/lib/use-clock";
import type { Snapshot } from "@/lib/telemetry-types";
import { usePolling } from "@/lib/use-polling";

const POLL_INTERVAL_MS = 15_000;
const RANGES: Array<{ id: InfrastructureRange; label: string; durationMS: number }> = [
  { id: "1h", label: "1 hour", durationMS: 60 * 60 * 1_000 },
  { id: "6h", label: "6 hours", durationMS: 6 * 60 * 60 * 1_000 },
  { id: "24h", label: "24 hours", durationMS: 24 * 60 * 60 * 1_000 },
];
const SERIES_COLORS = ["var(--blue)", "var(--green)", "var(--amber)", "var(--red)"];

function formatMetric(value: number | null, units: string) {
  if (value === null || !Number.isFinite(value)) return "—";
  if (units === "percent") return `${value.toFixed(value < 10 ? 1 : 0)}%`;
  if (Math.abs(value) >= 100) return Math.round(value).toLocaleString();
  return value.toFixed(value < 10 ? 2 : 1);
}

function seriesPath(
  points: InfrastructurePoint[],
  dimension: number,
  minAt: number,
  maxAt: number,
  maxValue: number,
) {
  const paths: string[] = [];
  let run: string[] = [];
  for (const point of points) {
    const value = point.values[dimension];
    const at = Date.parse(point.at);
    if (value === null || !Number.isFinite(value) || !Number.isFinite(at)) {
      if (run.length > 0) paths.push(run.join(" "));
      run = [];
      continue;
    }
    const x = 44 + ((at - minAt) / Math.max(1, maxAt - minAt)) * 548;
    const y = 12 + (1 - value / Math.max(1, maxValue)) * 112;
    run.push(`${run.length === 0 ? "M" : "L"}${x.toFixed(1)} ${y.toFixed(1)}`);
  }
  if (run.length > 0) paths.push(run.join(" "));
  return paths;
}

function InfrastructureChartPanel({ chart }: { chart: InfrastructureChart }) {
  const points = useMemo(() => orderedPoints(chart), [chart]);
  const latest = latestValues(chart);
  const finiteValues = points.flatMap(({ values }) => values.filter((value): value is number => value !== null && Number.isFinite(value)));
  const maxValue = chart.units === "percent" ? 100 : Math.max(1, ...finiteValues);
  const times = points.map(({ at }) => Date.parse(at)).filter(Number.isFinite);
  const minAt = Math.min(...times);
  const maxAt = Math.max(...times);
  const current = latest.map(({ dimension, value }) => `${dimension} ${formatMetric(value, chart.units)}`).join(" · ");
  return (
    <section className="graph-panel">
      <div className="chart-head">
        <h3>{chart.title}</h3>
        <span className="chart-current">{current || "no samples"}</span>
      </div>
      {points.length === 0 || !Number.isFinite(minAt) || !Number.isFinite(maxAt) ? (
        <div className="chart-empty">No samples in this range</div>
      ) : (
        <div className="chart-plot">
          <svg viewBox="0 0 606 146" width="100%" role="img" aria-label={`${chart.title}: ${current}`}>
            {[12, 68, 124].map((y) => <line key={y} className="grid-line" x1="44" x2="592" y1={y} y2={y} />)}
            <text className="tick-label" x="38" y="16" textAnchor="end">{formatMetric(maxValue, chart.units)}</text>
            <text className="tick-label" x="38" y="128" textAnchor="end">0</text>
            {chart.dimensions.map((dimension, index) =>
              seriesPath(points, index, minAt, maxAt, maxValue).map((path, run) => (
                <path
                  key={`${dimension}:${run}`}
                  d={path}
                  fill="none"
                  stroke={SERIES_COLORS[index % SERIES_COLORS.length]}
                  strokeWidth="1.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              )),
            )}
          </svg>
        </div>
      )}
      <ul className="graph-legend">
        {chart.dimensions.map((dimension, index) => (
          <li key={dimension}><span className="swatch" style={{ background: SERIES_COLORS[index % SERIES_COLORS.length] }} />{dimension}</li>
        ))}
      </ul>
    </section>
  );
}

function HostCard({ host }: { host: InfrastructureHost }) {
  const { formatDate, formatTime } = useClock();
  const status = hostStatus(host);
  const services = serviceStatuses(host);
  return (
    <section className="host-card" aria-label={`${host.title} host`}>
      <div className="host-card-head">
        <h3>{host.title}</h3>
        <span className={`host-state ${status.tone}`}>
          <span className={`dot dot-${status.tone}`} aria-hidden="true" />
          {status.label}
        </span>
      </div>
      <p className="host-role">{host.role}</p>
      {services.length > 0 && (
        <div className="host-services">
          {services.map(({ name, label, tone }) => (
            <div className="stat" key={name}>
              <span>{name}</span>
              <strong className={tone}>{label}</strong>
            </div>
          ))}
        </div>
      )}
      {!host.reachable && (
        <p className="host-note" title={host.last_collected_at ? formatDate(host.last_collected_at) : undefined}>
          {host.last_collected_at
            ? `No samples since ${formatTime(host.last_collected_at)}`
            : "Has never streamed metrics to the compute parent"}
        </p>
      )}
    </section>
  );
}

export function InfrastructureMonitoringPanel({
  active,
  polling = true,
  hostSnapshot,
}: {
  active: boolean;
  polling?: boolean;
  hostSnapshot: Snapshot | null;
}) {
  const [range, setRange] = useState<InfrastructureRange>("1h");
  const [snapshot, setSnapshot] = useState<InfrastructureSnapshot | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const { formatDate, formatTime } = useClock();

  const refresh = useCallback(async (signal: AbortSignal) => {
    try {
      const response = await fetch(`/api/admin/telemetry/infrastructure?range=${range}`, { cache: "no-store" });
      const next = await response.json() as InfrastructureSnapshot;
      if (!response.ok || !Array.isArray(next.charts)) throw new Error("infrastructure unavailable");
      if (!signal.aborted) {
        setSnapshot(next);
        setUnavailable(false);
      }
    } catch {
      if (!signal.aborted) setUnavailable(true);
    }
  }, [range]);
  // Without polling the panel still loads once when it becomes active.
  usePolling(refresh, polling ? POLL_INTERVAL_MS : null, { enabled: active, immediate: true });

  const hosts = infrastructureHosts(snapshot);
  const selectedRange = RANGES.find(({ id }) => id === range) ?? RANGES[0];
  return (
    <div className="infrastructure-view">
      <div className="graph-controls">
        <div className="range-picker" role="group" aria-label="Infrastructure time range">
          {RANGES.map((option) => (
            <button
              key={option.id}
              type="button"
              className="tab-chip"
              aria-pressed={range === option.id}
              onClick={() => setRange(option.id)}
            >
              {option.label}
            </button>
          ))}
        </div>
        <span className="table-note" title={snapshot ? formatDate(snapshot.observed_at) : undefined}>
          {snapshot ? `Netdata observed ${formatTime(snapshot.observed_at)}` : "Netdata metrics"}
        </span>
      </div>
      {unavailable && (
        <div className="notice error" role="alert">
          Infrastructure metrics are unavailable{snapshot ? " — showing the last successful result" : ""}
        </div>
      )}
      {snapshot === null && !unavailable ? (
        <p className="empty-note">Loading infrastructure metrics…</p>
      ) : (
        <>
          <div className="host-grid" aria-label="Host health">
            {hosts.map((host) => <HostCard key={host.id} host={host} />)}
          </div>
          {hosts.map((host) => {
            const charts = host.charts.filter(({ id }) => id !== HEALTH_CHART_ID);
            const resources = charts.filter(({ id }) => HOST_RESOURCE_CHART_IDS.has(id));
            const services = charts.filter(({ id }) => !HOST_RESOURCE_CHART_IDS.has(id));
            const startup = host.id === "compute" && hostSnapshot;
            if (charts.length === 0 && !startup) return null;
            return (
              <section key={host.id} aria-label={`${host.title} metrics`}>
                <h2 className="section-label">{host.title}</h2>
                <div className="graph-grid">
                  {resources.map((chart) => <InfrastructureChartPanel key={chart.id} chart={chart} />)}
                  {startup && (
                    <SandboxStartupGraph
                      sandboxes={hostSnapshot.sandboxes}
                      observedAt={hostSnapshot.observed_at}
                      durationMS={selectedRange.durationMS}
                      rangeLabel={selectedRange.label}
                    />
                  )}
                  {services.map((chart) => <InfrastructureChartPanel key={chart.id} chart={chart} />)}
                </div>
              </section>
            );
          })}
        </>
      )}
      <div className="infrastructure-diagnostics">
        <PlatformDiagnosticsPanel active={active} polling={polling} />
      </div>
    </div>
  );
}
