import type { InfrastructureChart, InfrastructureHost, InfrastructureSnapshot } from "@/lib/infrastructure-types";

export const HEALTH_CHART_ID = "fire_fuse.health";
export const HOST_RESOURCE_CHART_IDS = new Set(["system.cpu", "system.ram", "disk_space./"]);

export type ServiceTone = "ok" | "bad" | "dim";

export interface ServiceStatus {
  name: string;
  label: string;
  tone: ServiceTone;
}

// The observer's per-host view, or — from an observer that predates it — the
// legacy chart list presented as the one compute host it described.
export function infrastructureHosts(snapshot: InfrastructureSnapshot | null): InfrastructureHost[] {
  if (!snapshot) return [];
  if (Array.isArray(snapshot.hosts)) return snapshot.hosts;
  return [{ id: "compute", title: "Compute", role: "Firecracker runtime", reachable: true, charts: snapshot.charts }];
}

export function orderedPoints(chart: InfrastructureChart) {
  return [...chart.points].sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

export function latestValues(chart: InfrastructureChart | undefined) {
  if (!chart) return [];
  const latest = orderedPoints(chart).at(-1);
  return chart.dimensions.map((dimension, index) => ({
    dimension,
    value: latest?.values[index] ?? null,
  }));
}

// A service's last reading only counts while its host is still reporting;
// otherwise the newest value in the window could be hours old.
export function serviceStatuses(host: InfrastructureHost): ServiceStatus[] {
  const health = latestValues(host.charts.find(({ id }) => id === HEALTH_CHART_ID));
  return health.map(({ dimension, value }) => {
    if (!host.reachable) return { name: dimension, label: "unknown", tone: "dim" };
    if (value === null || !Number.isFinite(value)) return { name: dimension, label: "no data", tone: "dim" };
    return value >= 1
      ? { name: dimension, label: "ready", tone: "ok" }
      : { name: dimension, label: "down", tone: "bad" };
  });
}

export function hostStatus(host: InfrastructureHost): { label: string; tone: "ok" | "warn" | "bad" } {
  if (!host.reachable) return { label: host.last_collected_at ? "unreachable" : "not connected", tone: "bad" };
  const services = serviceStatuses(host);
  return services.length === 0 || services.some(({ tone }) => tone !== "ok")
    ? { label: "degraded", tone: "warn" }
    : { label: "online", tone: "ok" };
}
