import "server-only";

import type { HostWorkspace, RequestEventPage, Snapshot } from "@/lib/telemetry-types";
import type { Sandbox, Session } from "@/lib/telemetry-types";
import type { InfrastructureRange, InfrastructureSnapshot } from "@/lib/infrastructure-types";

function statusConfiguration() {
  const token = process.env.FIRE_FUSE_STATUS_TOKEN;
  if (!token || token.length < 32) throw new Error("Fire Fuse status token is not configured");
  return { origin: observerOrigin(), token };
}

// The most Workspace IDs one scoped snapshot may name; the observer rejects
// more so the query stays inside proxy URL limits.
export const maxSnapshotWorkspaces = 256;

// Without workspaceIDs this is the operator's whole-host snapshot. With them
// the observer reads and returns only those Workspaces' records; an empty
// list still reports host readiness.
export async function getSnapshot(workspaceIDs?: readonly string[]): Promise<Snapshot<HostWorkspace>> {
  const { origin, token } = statusConfiguration();
  const endpoint = new URL("/snapshot", origin);
  if (workspaceIDs !== undefined) {
    if (workspaceIDs.length > maxSnapshotWorkspaces ||
        workspaceIDs.some((id) => !/^wsp_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$/.test(id))) {
      throw new TypeError("invalid snapshot Workspace scope");
    }
    endpoint.searchParams.set("workspace_ids", workspaceIDs.join(","));
  }
  const response = await fetch(endpoint, {
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    throw new Error(`Fire Fuse observer returned HTTP ${response.status}`);
  }
  const snapshot = (await response.json()) as Snapshot<HostWorkspace>;
  if (!snapshot || typeof snapshot.ready !== "boolean" ||
      !Array.isArray(snapshot.workspaces) || !Array.isArray(snapshot.sessions) ||
      !Array.isArray(snapshot.sandboxes)) {
    throw new Error("Fire Fuse observer returned an invalid snapshot");
  }
  return snapshot;
}

export async function getRequestEvents(search: URLSearchParams): Promise<RequestEventPage> {
  const { origin, token } = statusConfiguration();
  const endpoint = new URL("/requests", origin);
  endpoint.search = search.toString();
  const response = await fetch(endpoint.toString(), {
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    throw new Error(`Fire Fuse observer returned HTTP ${response.status}`);
  }
  const page = (await response.json()) as RequestEventPage;
  if (!page || !Array.isArray(page.items)) {
    throw new Error("Fire Fuse observer returned invalid request history");
  }
  return page;
}

export async function getInfrastructure(range: InfrastructureRange): Promise<InfrastructureSnapshot> {
  const { origin, token } = statusConfiguration();
  const endpoint = new URL("/infrastructure", origin);
  endpoint.searchParams.set("range", range);
  const response = await fetch(endpoint, {
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(15_000),
  });
  if (!response.ok) {
    throw new Error(`Fire Fuse observer returned HTTP ${response.status}`);
  }
  const snapshot = (await response.json()) as InfrastructureSnapshot;
  const validCharts = (charts: unknown) => Array.isArray(charts) &&
    charts.every((chart) => chart && typeof chart.id === "string" &&
      Array.isArray(chart.dimensions) && Array.isArray(chart.points));
  if (!snapshot || snapshot.range !== range || !validCharts(snapshot.charts) ||
      (snapshot.hosts !== undefined && (!Array.isArray(snapshot.hosts) ||
        snapshot.hosts.some((host) => !host || typeof host.id !== "string" ||
          typeof host.reachable !== "boolean" || !validCharts(host.charts))))) {
    throw new Error("Fire Fuse observer returned invalid infrastructure metrics");
  }
  return snapshot;
}

export async function terminateSession(id: string): Promise<Session> {
  if (!/^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$/.test(id)) throw new TypeError("invalid Session ID");
  const token = process.env.FIRE_FUSE_RUNTIME_TOKEN;
  if (!token || token.length < 32) throw new Error("Fire Fuse runtime token is not configured");
  const endpoint = new URL(`/sessions/${id}`, observerOrigin());
  const response = await fetch(endpoint, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(30_000),
  });
  if (response.status === 404) throw new Error("runtime_session_not_found");
  if (!response.ok) throw new Error(`Fire Fuse runtime returned HTTP ${response.status}`);
  return response.json() as Promise<Session>;
}

export async function terminateSandbox(id: string): Promise<Sandbox> {
  if (!/^sbx-[0-9a-f]{32}$/.test(id)) throw new TypeError("invalid Sandbox ID");
  const token = process.env.FIRE_FUSE_RUNTIME_TOKEN;
  if (!token || token.length < 32) throw new Error("Fire Fuse runtime token is not configured");
  const endpoint = new URL(`/sandboxes/${id}`, observerOrigin());
  const response = await fetch(endpoint, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(30_000),
  });
  if (response.status === 401 || response.status === 403) throw new Error("runtime_sandbox_unauthorized");
  if (response.status === 404) throw new Error("runtime_sandbox_not_found");
  if (response.status === 409) throw new Error("runtime_sandbox_invalid_state");
  if (!response.ok) throw new Error(`Fire Fuse runtime returned HTTP ${response.status}`);
  return response.json() as Promise<Sandbox>;
}

export async function crashSandbox(id: string): Promise<Sandbox> {
  if (!/^sbx-[0-9a-f]{32}$/.test(id)) throw new TypeError("invalid Sandbox ID");
  const token = process.env.FIRE_FUSE_RUNTIME_TOKEN;
  if (!token || token.length < 32) throw new Error("Fire Fuse runtime token is not configured");
  const endpoint = new URL(`/sandboxes/${id}/crash`, observerOrigin());
  const response = await fetch(endpoint, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
    signal: AbortSignal.timeout(30_000),
  });
  if (response.status === 401 || response.status === 403) throw new Error("runtime_sandbox_unauthorized");
  if (response.status === 404) throw new Error("runtime_sandbox_not_found");
  if (response.status === 409) throw new Error("runtime_sandbox_invalid_state");
  if (!response.ok) throw new Error(`Fire Fuse runtime returned HTTP ${response.status}`);
  return response.json() as Promise<Sandbox>;
}

function observerOrigin(): URL {
  const raw = process.env.FIRE_FUSE_OBSERVER_URL;
  if (!raw) throw new Error("Fire Fuse observer origin is not configured");
  const url = new URL(raw);
  if (url.username || url.password || url.search || url.hash || (url.pathname !== "/" && url.pathname !== "")) {
    throw new Error("FIRE_FUSE_OBSERVER_URL must be an origin");
  }
  const local = url.protocol === "http:" && ["127.0.0.1", "::1", "localhost"].includes(url.hostname);
  if (url.protocol !== "https:" && !local) throw new Error("FIRE_FUSE_OBSERVER_URL must use HTTPS");
  return url;
}
