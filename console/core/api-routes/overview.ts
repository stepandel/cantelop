import type { APIRoute } from "../api-deployments/artifact";
import type { ActiveReleaseRoutes } from "../db/release-repository";
import type { RouteTraffic } from "../observability/repository";

export interface AppRouteTraffic {
  requests: number;
  client_errors: number;
  server_errors: number;
  median_duration_us?: number;
  last_seen_at: string;
  // Session work the route's requests started, and how many attempts failed.
  messages: number;
  failed_attempts: number;
}

export interface AppRoute {
  method: string;
  path: string;
  traffic?: AppRouteTraffic;
}

/**
 * An App's routes beside their observed traffic.
 *
 * `declaration` says how far `routes` can be trusted: "declared" lists the
 * ingress the active release's generated Edge Worker serves; "unsupported"
 * means the live release predates SDK 1.0. Then, and with no release at all,
 * `routes` is empty and every observed route is in `observed_only`.
 */
export interface AppRouteOverview {
  release?: { id: string; version: number };
  declaration: "declared" | "unsupported" | "none";
  routes: AppRoute[];
  observed_only: AppRoute[];
  // Absent when the observability store could not be read.
  traffic_window_hours?: number;
}

export const maxObservedOnlyRoutes = 25;

export function appRouteOverview(
  active: ActiveReleaseRoutes | null,
  traffic: { routes: readonly RouteTraffic[]; windowHours: number } | null,
): AppRouteOverview {
  const declared: readonly APIRoute[] = active?.routes ?? [];
  const observed = new Map((traffic?.routes ?? []).map((route) => [routeKey(route), route]));
  const routes = declared.map((route): AppRoute => {
    const seen = observed.get(routeKey(route));
    observed.delete(routeKey(route));
    return { method: route.method, path: route.path, ...(seen === undefined ? {} : { traffic: publicTraffic(seen) }) };
  });
  return {
    ...(active === null ? {} : { release: { id: active.releaseID, version: active.version } }),
    declaration: routeDeclaration(active),
    routes,
    observed_only: [...observed.values()].slice(0, maxObservedOnlyRoutes)
      .map((route) => ({ method: route.method, path: route.path, traffic: publicTraffic(route) })),
    ...(traffic === null ? {} : { traffic_window_hours: traffic.windowHours }),
  };
}

function routeDeclaration(active: ActiveReleaseRoutes | null): AppRouteOverview["declaration"] {
  if (active === null) return "none";
  return active.routes === null ? "unsupported" : "declared";
}

function routeKey(route: { method: string; path: string }): string {
  return `${route.method} ${route.path}`;
}

function publicTraffic(route: RouteTraffic): AppRouteTraffic {
  return {
    requests: route.requests,
    client_errors: route.clientErrors,
    server_errors: route.serverErrors,
    ...(route.medianDurationUs === null ? {} : { median_duration_us: route.medianDurationUs }),
    last_seen_at: route.lastSeenAt.toISOString(),
    messages: route.messages,
    failed_attempts: route.failedAttempts,
  };
}
