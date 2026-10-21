import { expect, test } from "bun:test";

import { appRouteOverview, maxObservedOnlyRoutes } from "./overview";

const releaseID = "rel_0123456789abcdef0123456789abcdef";
const lastSeenAt = new Date("2026-09-20T10:00:00Z");
const seen = (method, path, overrides = {}) => ({
  method, path, requests: 3, clientErrors: 1, serverErrors: 0, medianDurationUs: 1200, lastSeenAt, messages: 2, failedAttempts: 1, ...overrides,
});
const active = (routes) => ({ releaseID, version: 7, routes });

test("joins declared routes with their traffic and separates undeclared paths", () => {
  expect(appRouteOverview(
    active([{ method: "POST", path: "/chat" }, { method: "GET", path: "/health" }]),
    { routes: [seen("GET", "/wp-admin"), seen("POST", "/chat", { medianDurationUs: null })], windowHours: 72 },
  )).toEqual({
    release: { id: releaseID, version: 7 },
    declaration: "declared",
    routes: [
      { method: "POST", path: "/chat", traffic: { requests: 3, client_errors: 1, server_errors: 0, last_seen_at: "2026-09-20T10:00:00.000Z", messages: 2, failed_attempts: 1 } },
      { method: "GET", path: "/health" },
    ],
    observed_only: [{
      method: "GET", path: "/wp-admin",
      traffic: { requests: 3, client_errors: 1, server_errors: 0, median_duration_us: 1200, last_seen_at: "2026-09-20T10:00:00.000Z", messages: 2, failed_attempts: 1 },
    }],
    traffic_window_hours: 72,
  });
});

test("a live release that predates SDK 1.0 lists only observed traffic", () => {
  const traffic = { routes: [seen("GET", "/health")], windowHours: 72 };
  expect(appRouteOverview(active(null), traffic)).toMatchObject({
    declaration: "unsupported", routes: [], observed_only: [{ method: "GET", path: "/health" }],
  });
  expect(appRouteOverview(null, traffic)).toMatchObject({ declaration: "none", observed_only: [{ path: "/health" }] });
  expect(appRouteOverview(null, traffic).release).toBeUndefined();
});

test("survives an unavailable observability store and bounds undeclared paths", () => {
  const overview = appRouteOverview(active([{ method: "GET", path: "/health" }]), null);
  expect(overview).toEqual({
    release: { id: releaseID, version: 7 }, declaration: "declared",
    routes: [{ method: "GET", path: "/health" }], observed_only: [],
  });
  const noisy = Array.from({ length: maxObservedOnlyRoutes + 5 }, (_, index) => seen("GET", `/probe-${index}`));
  expect(appRouteOverview(active([]), { routes: noisy, windowHours: 72 }).observed_only).toHaveLength(maxObservedOnlyRoutes);
});
