import { expect, test } from "bun:test";

import { hostStatus, infrastructureHosts, latestValues, serviceStatuses } from "../lib/infrastructure-hosts.ts";

const health = (dimensions, rows) => ({
  id: "fire_fuse.health",
  title: "Service health",
  units: "status",
  dimensions,
  points: rows.map(([at, ...values]) => ({ at, values })),
});

test("presents a legacy observer's charts as the compute host", () => {
  const chart = health(["fire-fuse"], [["2026-09-28T12:00:00Z", 1]]);
  const hosts = infrastructureHosts({ observed_at: "2026-09-28T12:00:00Z", range: "1h", charts: [chart] });

  expect(hosts).toEqual([{ id: "compute", title: "Compute", role: "Firecracker runtime", reachable: true, charts: [chart] }]);
  expect(infrastructureHosts(null)).toEqual([]);
});

test("does not carry a service value through a missing sample", () => {
  const host = {
    id: "storage", title: "Storage", role: "", reachable: true,
    charts: [health(["Redis", "MinIO"], [
      ["2026-09-28T12:00:30Z", 1, 0],
      ["2026-09-28T12:00:00Z", 0, 1],
      ["2026-09-28T12:01:00Z", null, 0],
    ])],
  };

  expect(serviceStatuses(host)).toEqual([
    { name: "Redis", label: "no data", tone: "dim" },
    { name: "MinIO", label: "down", tone: "bad" },
  ]);
  expect(hostStatus(host)).toEqual({ label: "degraded", tone: "warn" });
  expect(latestValues(host.charts[0])).toEqual([
    { dimension: "Redis", value: null },
    { dimension: "MinIO", value: 0 },
  ]);
});

test("a reachable host without a health chart is degraded", () => {
  const host = { id: "storage", title: "Storage", role: "", reachable: true, charts: [] };
  expect(serviceStatuses(host)).toEqual([]);
  expect(hostStatus(host)).toEqual({ label: "degraded", tone: "warn" });
});

test("never reports an unreachable host's last reading as current", () => {
  const stale = {
    id: "builder", title: "Builder", role: "", reachable: false, last_collected_at: "2026-09-28T11:00:00Z",
    charts: [health(["Control worker"], [["2026-09-28T11:00:00Z", 1]])],
  };

  expect(serviceStatuses(stale)).toEqual([{ name: "Control worker", label: "unknown", tone: "dim" }]);
  expect(hostStatus(stale)).toEqual({ label: "unreachable", tone: "bad" });
  expect(hostStatus({ ...stale, last_collected_at: undefined, charts: [] })).toEqual({ label: "not connected", tone: "bad" });
});

test("a reachable host with every service ready is online", () => {
  const host = {
    id: "compute", title: "Compute", role: "", reachable: true,
    charts: [health(["fire-fuse", "JuiceFS gateway"], [["2026-09-28T12:00:00Z", 1, 1]])],
  };

  expect(hostStatus(host)).toEqual({ label: "online", tone: "ok" });
});
