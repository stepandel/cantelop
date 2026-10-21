import type { RouteDetail, RouteRecentRequest } from "../observability/repository";

/** One route of an App as its detail page reads it. Wire shape of RouteDetail. */
export interface AppRouteDetail {
  method: string;
  path: string;
  declared: boolean;
  http: {
    requests: number;
    status_counts: { status: number; requests: number }[];
    p50_duration_us?: number;
    p95_duration_us?: number;
    first_seen_at?: string;
    last_seen_at?: string;
  };
  downstream: {
    messages: number;
    attempts: number;
    completed: number;
    failed: number;
    pending: number;
    p50_time_to_start_us?: number;
    p50_time_to_finish_us?: number;
    error_codes: { code: string; attempts: number }[];
  };
  // The latest requests whatever their outcome, then the latest that failed.
  recent_requests: AppRouteRecentRequest[];
  recent_failures: AppRouteRecentRequest[];
  traffic_window_hours: number;
}

export interface AppRouteRecentRequest {
  id: string;
  trace_id: string;
  workspace_id?: string;
  status_code?: number;
  duration_us?: number;
  received_at: string;
  attempt_outcome?: string;
  attempt_error_code?: string;
  // The Session and message the request dispatched to, if it dispatched one.
  session_id?: string;
  message_id?: string;
}

const optional = <T>(key: string, value: T | null | undefined) => (value === null || value === undefined ? {} : { [key]: value });

function recentRequest(request: RouteRecentRequest): AppRouteRecentRequest {
  return {
    id: request.id,
    trace_id: request.traceId,
    ...optional("workspace_id", request.workspaceId),
    ...optional("status_code", request.statusCode),
    ...optional("duration_us", request.durationUs),
    received_at: request.receivedAt.toISOString(),
    ...optional("attempt_outcome", request.attemptOutcome),
    ...optional("attempt_error_code", request.attemptErrorCode),
    ...optional("session_id", request.sessionId),
    ...optional("message_id", request.messageId),
  };
}

export function appRouteDetail(detail: RouteDetail, declared: boolean, windowHours: number): AppRouteDetail {
  return {
    method: detail.method,
    path: detail.path,
    declared,
    http: {
      requests: detail.http.requests,
      status_counts: detail.http.statusCounts,
      ...optional("p50_duration_us", detail.http.p50DurationUs),
      ...optional("p95_duration_us", detail.http.p95DurationUs),
      ...optional("first_seen_at", detail.http.firstSeenAt?.toISOString()),
      ...optional("last_seen_at", detail.http.lastSeenAt?.toISOString()),
    },
    downstream: {
      messages: detail.downstream.messages,
      attempts: detail.downstream.attempts,
      completed: detail.downstream.completed,
      failed: detail.downstream.failed,
      pending: detail.downstream.pending,
      ...optional("p50_time_to_start_us", detail.downstream.p50TimeToStartUs),
      ...optional("p50_time_to_finish_us", detail.downstream.p50TimeToFinishUs),
      error_codes: detail.downstream.errorCodes,
    },
    recent_requests: detail.recentRequests.map(recentRequest),
    recent_failures: detail.recentFailures.map(recentRequest),
    traffic_window_hours: windowHours,
  };
}
