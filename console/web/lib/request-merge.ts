import type { AppRequestInvocation, AppRequestLog, RequestEvent } from "@/lib/telemetry-types";

// One request crosses two recorders. The edge dispatcher mints a request ID,
// logs the Worker invocation against it (method, path, application logs), and
// forwards the same ID to the workspace gateway on X-Cantelop-Edge-Request-ID;
// the gateway writes it into its durable request log alongside the workspace
// and session that served the call. The console used to show those two halves
// as two separate "requests" tables on two separate screens. They are one row.
//
// The two sources have very different reach, which decides which half wins a
// column: the gateway log is durable and paginated back through the whole
// history, while edge observability only holds the last 6h (and at most 100
// invocations). So the gateway leg supplies status and duration wherever it
// exists — otherwise a row's numbers would change source as the reader pages
// backwards — and the edge leg contributes what the gateway cannot see. Both
// legs stay visible in the opened row.

export interface MergedRequest {
  /** Stable list key: the request ID when correlated, else the recorder's own. */
  key: string;
  requestID?: string;
  eventID?: string;
  /** Gateway pagination cursor; absent on an edge-only row. */
  sequence?: number;
  receivedAt: string;
  workspaceID?: string;
  sessionID?: string;
  method?: string;
  path?: string;
  /** Gateway status where recorded, else the edge's. */
  status?: number;
  durationMS?: number;
  /** Present when the edge and the gateway disagree, or the gateway is silent. */
  edgeStatus?: number;
  edgeDurationMS?: number;
  logs: AppRequestLog[];
}

export function mergeRequests(
  events: RequestEvent[],
  invocations: AppRequestInvocation[],
): MergedRequest[] {
  const edgeByRequestID = new Map(invocations.map((invocation) => [invocation.request_id, invocation]));
  const claimed = new Set<string>();

  const merged: MergedRequest[] = events.map((event) => {
    const edge = event.request_id === undefined ? undefined : edgeByRequestID.get(event.request_id);
    if (edge) claimed.add(edge.request_id);
    return {
      key: event.request_id ?? event.event_id,
      ...(event.request_id === undefined ? {} : { requestID: event.request_id }),
      eventID: event.event_id,
      sequence: event.sequence,
      receivedAt: event.received_at,
      workspaceID: event.workspace_id,
      ...(event.session_id === undefined ? {} : { sessionID: event.session_id }),
      ...(edge?.method === undefined ? {} : { method: edge.method }),
      ...(edge?.path === undefined ? {} : { path: edge.path }),
      status: event.status_code,
      durationMS: event.duration_ms,
      ...(edge?.status === undefined ? {} : { edgeStatus: edge.status }),
      ...(edge?.duration_ms === undefined ? {} : { edgeDurationMS: edge.duration_ms }),
      logs: edge?.logs ?? [],
    };
  });

  // An invocation with no gateway leg is a real request that never reached a
  // workspace — rejected at the edge, or aimed at a route the gateway does not
  // record. Dropping it would make the table quietly incomplete.
  for (const invocation of invocations) {
    if (claimed.has(invocation.request_id)) continue;
    merged.push({
      key: invocation.request_id,
      requestID: invocation.request_id,
      receivedAt: invocation.timestamp,
      ...(invocation.method === undefined ? {} : { method: invocation.method }),
      ...(invocation.path === undefined ? {} : { path: invocation.path }),
      ...(invocation.status === undefined ? {} : { status: invocation.status, edgeStatus: invocation.status }),
      ...(invocation.duration_ms === undefined
        ? {}
        : { durationMS: invocation.duration_ms, edgeDurationMS: invocation.duration_ms }),
      logs: invocation.logs,
    });
  }

  return merged.sort((left, right) => {
    const byTime = Date.parse(right.receivedAt) - Date.parse(left.receivedAt);
    // Two events inside the same millisecond still have a total order in the
    // gateway log; falling back to it keeps the list from reshuffling on poll.
    if (byTime !== 0) return byTime;
    return (right.sequence ?? 0) - (left.sequence ?? 0);
  });
}
