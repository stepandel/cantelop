import type { AppRequestInvocation, RequestEvent, Sandbox, Session, Snapshot, Workspace } from "@/lib/telemetry-types";

// Synthetic 24 hours of one App across two Workspaces, sized to the case the
// graph view exists for: enough records that the tables have stopped being
// readable. Everything is derived from a fixed seed and a fixed observation
// time, so the preview renders identically on every request and in every
// screenshot.

const OBSERVED_AT = "2026-08-04T09:41:52Z";
const OBSERVED_MS = Date.parse(OBSERVED_AT);
const DAY_MS = 24 * 60 * 60 * 1_000;

const APP_ID = "app_0123456789abcdef0123456789abcdef";

// Two Workspaces of very different weight: the workspace picker and the
// workspace column only earn their space when the rows they separate differ.
// The incident below belongs to production alone, so filtering to staging has
// to visibly flatten the graphs.
const WORKSPACES: Array<Workspace & { slug: string }> = [
  {
    id: "wsp_base_agent_production",
    app_id: APP_ID,
    slug: "production",
    created_at: "2026-06-02T17:14:08.000Z",
    updated_at: "2026-06-02T17:14:08.000Z",
  },
  {
    id: "wsp_base_agent_staging",
    app_id: APP_ID,
    slug: "staging",
    created_at: "2026-07-19T09:41:52.000Z",
    updated_at: "2026-07-19T09:41:52.000Z",
  },
];
const [PRODUCTION, STAGING] = WORKSPACES;
const BUILDS = [
  "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  "sha256:fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9",
];

// The incident the fixture is built around: a bad build rolls out at T-5h,
// boots get slow, 5xx climbs, two sandboxes fail, and a rollback at T-3h20m
// puts everything back. A flat random walk would show that the charts render;
// this shows whether they read.
const INCIDENT_START = OBSERVED_MS - 5 * 60 * 60 * 1_000;
const INCIDENT_END = OBSERVED_MS - 3.33 * 60 * 60 * 1_000;

function mulberry32(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let value = Math.imul(state ^ (state >>> 15), 1 | state);
    value = (value + Math.imul(value ^ (value >>> 7), 61 | value)) ^ value;
    return ((value ^ (value >>> 14)) >>> 0) / 4294967296;
  };
}

// Box–Muller, for the lifetime and startup spreads. Both are long-tailed in
// practice — a teardown is seconds, a held session is hours — so they are
// drawn lognormally rather than uniformly.
function gaussian(random: () => number) {
  const u = Math.max(random(), 1e-9);
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * random());
}

const iso = (at: number) => new Date(Math.round(at)).toISOString().replace(".000Z", "Z");
const id = (prefix: string, index: number) => `${prefix}_01k1${index.toString(36).padStart(6, "0")}`;

// Request rate over the day: quiet overnight, a working-hours ramp, and a
// burst during the incident as clients retry.
function trafficWeight(at: number) {
  const hoursBack = (OBSERVED_MS - at) / (60 * 60 * 1_000);
  const diurnal = 0.35 + 0.65 * Math.max(0, Math.cos((hoursBack / 24) * Math.PI * 1.4));
  const burst = at >= INCIDENT_START && at <= INCIDENT_END ? 1.8 : 1;
  return diurnal * burst;
}

// Arrivals follow the same diurnal curve as traffic, by rejection sampling —
// concurrency should rise and fall with the working day rather than sit flat.
function arrivalTime(random: () => number) {
  for (let attempt = 0; attempt < 24; attempt += 1) {
    const at = OBSERVED_MS - DAY_MS + random() * DAY_MS;
    if (random() < trafficWeight(at)) return at;
  }
  return OBSERVED_MS - DAY_MS + random() * DAY_MS;
}

function buildSandboxes(random: () => number, workspace: Workspace, count: number, prefix: string, degrades: boolean): Sandbox[] {
  const sandboxes: Sandbox[] = [];
  for (let index = 0; index < count; index += 1) {
    const createdAt = arrivalTime(random);
    const degraded = degrades && createdAt >= INCIDENT_START && createdAt <= INCIDENT_END;
    // Median lifetime ~20 minutes, mean ~37: most sandboxes are torn down
    // shortly after their session goes idle, a few are held for hours.
    const lifetime = Math.exp(7.1 + 1.1 * gaussian(random)) * 1_000;
    const endsAt = createdAt + lifetime;
    const alive = endsAt > OBSERVED_MS;
    const failed = degraded && random() < 0.22;
    const startupMS = (degraded ? 3_200 + random() * 6_400 : 780 + random() ** 2 * 2_200) * (random() < 0.06 ? 2.4 : 1);

    sandboxes.push({
      id: id(`sbx${prefix}`, index),
      workspace_id: workspace.id,
      app_id: APP_ID,
      profile: "standard-2",
      build_id: degraded ? BUILDS[1] : BUILDS[0],
      vcpus: 2,
      memory_mib: 2048,
      state: failed ? "failed" : alive ? "running" : "terminated",
      runtime_timing: {
        status: alive && !failed ? "running" : "stopped",
        started_at: iso(createdAt),
        ...(alive && !failed ? {} : { stopped_at: iso(Math.min(endsAt, OBSERVED_MS)) }),
      },
      ...(failed ? { failure: { reason: "runtime_exited" } } : {}),
      // A failed boot never completes, so it carries no startup record.
      ...(failed ? {} : { startup: { completed_at: iso(createdAt + startupMS), duration_ms: Math.round(startupMS * 10) / 10 } }),
      ...(alive && !failed
        ? {
            usage: {
              observed_at: OBSERVED_AT,
              cpu_usage_us: Math.round(4_000_000 + random() * 40_000_000),
              memory_current_bytes: Math.round((320 + random() * 900) * 1024 * 1024),
              guest_memory_used_bytes: Math.round((80 + random() * 600) * 1024 * 1024),
            },
          }
        : {}),
      created_at: iso(createdAt),
      updated_at: iso(alive && !failed ? OBSERVED_MS - random() * 120_000 : Math.min(endsAt, OBSERVED_MS)),
    });
  }
  return sandboxes.sort((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at));
}

function buildSessions(random: () => number, sandboxes: Sandbox[], workspace: Workspace, count: number, prefix: string): Session[] {
  const live = sandboxes.filter(({ state }) => state === "running");
  const sessions: Session[] = [];
  for (let index = 0; index < count; index += 1) {
    const createdAt = arrivalTime(random);
    // Sessions outlive their sandboxes, and state is derived from the binding:
    // exactly as many sessions are active as there are running sandboxes, and
    // the rest are idle until a request reactivates them onto a new one.
    const active = index < live.length;
    sessions.push({
      id: id(`ses${prefix}`, index),
      workspace_id: workspace.id,
      app_id: APP_ID,
      build_id: BUILDS[0],
      keep_alive_seconds: 600,
      state: active ? "active" : "idle",
      created_at: iso(createdAt),
      updated_at: iso(active ? OBSERVED_MS - random() * 300_000 : createdAt + random() * 3 * 60 * 60 * 1_000),
    });
  }
  return sessions.sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at));
}

function buildRequestEvents(
  random: () => number,
  sessions: Session[],
  workspace: Workspace,
  rate: number,
  prefix: string,
  degrades: boolean,
): RequestEvent[] {
  const events: RequestEvent[] = [];
  const step = 90 * 1_000;
  for (let at = OBSERVED_MS - DAY_MS; at < OBSERVED_MS; at += step) {
    const expected = trafficWeight(at) * rate;
    const count = Math.floor(expected + random());
    const degraded = degrades && at >= INCIDENT_START && at <= INCIDENT_END;
    for (let index = 0; index < count; index += 1) {
      const receivedAt = at + random() * step;
      const roll = random();
      const statusCode = degraded
        ? roll < 0.24 ? 503 : roll < 0.29 ? 504 : roll < 0.33 ? 429 : 200
        : roll < 0.014 ? 404 : roll < 0.02 ? 500 : roll < 0.035 ? 304 : 200;
      // A 5xx during the incident is an activation timeout, so it is slow;
      // a 404 never reaches a sandbox, so it is fast.
      const durationMS = statusCode >= 500
        ? 2_400 + random() * 7_000
        : statusCode === 404
          ? 3 + random() * 12
          : (degraded ? 180 : 14) + random() ** 3 * (degraded ? 2_600 : 420);
      events.push({
        sequence: 0,
        event_id: `preview-${prefix}-${events.length}`,
        // A few events predate the request ID upgrade and cannot be joined to
        // an edge invocation — the opened row says so rather than implying the
        // Worker emitted nothing.
        ...(random() < 0.96 ? { request_id: `${prefix}${events.length.toString(36).padStart(6, "0")}${"c".repeat(20)}` } : {}),
        workspace_id: workspace.id,
        ...(random() < 0.94 ? { session_id: sessions[Math.floor(random() * sessions.length)].id } : {}),
        received_at: iso(receivedAt),
        status_code: statusCode,
        duration_ms: Math.round(durationMS * 10) / 10,
      });
    }
  }
  return events;
}

// Routes the Session runtime serves, weighted the way a real one is: mostly one hot
// endpoint, a long tail of everything else.
const ROUTES: Array<{ method: string; path: string }> = [
  { method: "POST", path: "/v1/messages" },
  { method: "POST", path: "/v1/messages" },
  { method: "POST", path: "/v1/messages" },
  { method: "GET", path: "/v1/sessions" },
  { method: "POST", path: "/v1/tools/search" },
  { method: "GET", path: "/healthz" },
];

// The edge leg. Worker observability only holds the last 6h and at most 100
// invocations, so the preview covers the newest slice of the gateway log and
// leaves everything older gateway-only — which is exactly what the merged
// table has to degrade into on a real App.
function buildInvocations(random: () => number, events: RequestEvent[]): AppRequestInvocation[] {
  const windowStart = OBSERVED_MS - 6 * 60 * 60 * 1_000;
  const invocations: AppRequestInvocation[] = [];
  for (const event of events) {
    if (invocations.length >= 96) break;
    if (event.request_id === undefined || Date.parse(event.received_at) < windowStart) continue;
    const route = ROUTES[Math.floor(random() * ROUTES.length)];
    const receivedMS = Date.parse(event.received_at);
    invocations.push({
      request_id: event.request_id,
      timestamp: event.received_at,
      method: route.method,
      path: route.path,
      status: event.status_code,
      // The edge sees its own overhead on top of the workspace's response.
      duration_ms: Math.round((event.duration_ms + 2 + random() * 6) * 10) / 10,
      outcome: event.status_code >= 500 ? "exception" : "ok",
      logs: event.status_code >= 500
        ? [{
            timestamp: iso(receivedMS + event.duration_ms * 0.9),
            level: "error",
            message: `upstream activation failed after ${Math.round(event.duration_ms)}ms: workspace ${event.workspace_id} had no ready sandbox`,
          }]
        : random() < 0.3
          ? [{ timestamp: iso(receivedMS + 1), level: "log", message: `dispatch ${route.method} ${route.path}` }]
          : [],
    });
  }
  // Requests the edge answered on its own never reach a Workspace, so they
  // have no gateway leg to merge with and must still appear in the table.
  invocations.push(
    {
      request_id: "edge0000000000000000000000edge1",
      timestamp: iso(OBSERVED_MS - 214_000),
      method: "GET",
      path: "/.env",
      status: 404,
      duration_ms: 1.2,
      outcome: "ok",
      logs: [],
    },
    {
      request_id: "edge0000000000000000000000edge2",
      timestamp: iso(OBSERVED_MS - 1_902_000),
      method: "POST",
      path: "/v1/messages",
      status: 401,
      duration_ms: 2.9,
      outcome: "ok",
      logs: [{ timestamp: iso(OBSERVED_MS - 1_902_000), level: "warn", message: "rejected: missing bearer token" }],
    },
  );
  return invocations;
}

const random = mulberry32(0x5eed_1a2b);
const productionSandboxes = buildSandboxes(random, PRODUCTION, 104, "p", true);
const stagingSandboxes = buildSandboxes(random, STAGING, 14, "s", false);
const productionSessions = buildSessions(random, productionSandboxes, PRODUCTION, 66, "p");
const stagingSessions = buildSessions(random, stagingSandboxes, STAGING, 8, "s");

export const previewWorkspaces: Workspace[] = [
  { ...PRODUCTION, last_activity_at: iso(OBSERVED_MS - 41_000) },
  { ...STAGING, last_activity_at: iso(OBSERVED_MS - 2_612_000) },
];

export const previewSnapshot: Snapshot = {
  ready: true,
  observed_at: OBSERVED_AT,
  workspaces: previewWorkspaces,
  sessions: [...productionSessions, ...stagingSessions],
  sandboxes: [...productionSandboxes, ...stagingSandboxes],
};

// The gateway log is one sequence across the whole host, so the two Workspaces
// interleave into a single cursor-ordered stream, newest first.
export const previewEvents: RequestEvent[] = (() => {
  const events = [
    ...buildRequestEvents(random, productionSessions, PRODUCTION, 4, "p", true),
    ...buildRequestEvents(random, stagingSessions, STAGING, 0.5, "s", false),
  ].sort((left, right) => Date.parse(left.received_at) - Date.parse(right.received_at));
  events.forEach((event, index) => {
    event.sequence = index + 1;
  });
  return events.reverse();
})();

export const previewInvocations: AppRequestInvocation[] = buildInvocations(random, previewEvents);
