import type { SQLClient } from "../db/database";

/** Placement is an App property. A missing managed pool never falls back to the
 * ordinary runtime, where policy and watchdog enforcement may be unavailable. */
export function computePoolFetch(database: SQLClient, environment: NodeJS.ProcessEnv, request: typeof fetch = fetch): typeof fetch {
  const standard = endpoint(environment.CANTELOP_COMPUTE_INGRESS_URL);
  const standardToken = environment.CANTELOP_COMPUTE_CONTROL_TOKEN;
  return (async (input, init) => {
    const original = new Request(input, init);
    const url = new URL(original.url);
    const app = /^\/v1\/internal\/users\/usr_[0-9a-f]{32}\/apps\/(app_[0-9a-f]{32})(?:\/|$)/.exec(url.pathname)?.[1];
    if (url.origin !== standard || !app || original.headers.get("authorization") !== `Bearer ${standardToken}`) {
      throw new Error("invalid_compute_request");
    }
    const result = await database.query<{ compute_pool: string }>("SELECT compute_pool FROM cantelop_control.apps WHERE id=$1", [app]);
    const pool = result.rows[0]?.compute_pool;
    if (pool === "standard") return request(original);
    if (pool !== "managed") throw new Error("compute_placement_unavailable");
    const managed = endpoint(environment.CANTELOP_MANAGED_COMPUTE_INGRESS_URL);
    const token = environment.CANTELOP_MANAGED_COMPUTE_CONTROL_TOKEN;
    if (!/^[0-9a-f]{64}$/.test(token ?? "") || managed === standard) throw new Error("managed_compute_unavailable");
    const target = new URL(managed);
    target.pathname = url.pathname;
    target.search = url.search;
    const routed = new Request(target, original);
    routed.headers.set("Authorization", `Bearer ${token}`);
    return request(routed);
  }) as typeof fetch;
}

function endpoint(value: string | undefined): string {
  if (!value) throw new Error("compute_endpoint_unavailable");
  const url = new URL(value);
  if (!/^https?:$/.test(url.protocol) || url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("invalid_compute_endpoint");
  }
  return url.origin;
}
