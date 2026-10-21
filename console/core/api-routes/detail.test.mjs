import { expect, test } from "bun:test";

import { appRouteDetail } from "./detail";

const receivedAt = new Date("2026-09-21T12:00:00Z");

test("shapes a route detail for the wire, dropping absent measurements", () => {
  expect(appRouteDetail({
    method: "POST", path: "/chat",
    http: { requests: 5, statusCounts: [{ status: 202, requests: 4 }, { status: 400, requests: 1 }], p50DurationUs: 12_000, p95DurationUs: 27_000, firstSeenAt: receivedAt, lastSeenAt: receivedAt },
    downstream: { messages: 4, attempts: 5, completed: 2, failed: 2, pending: 1, p50TimeToStartUs: 1_500_000, p50TimeToFinishUs: null, errorCodes: [{ code: "model_timeout", attempts: 2 }] },
    recentRequests: [
      { id: "req_2", traceId: "t2", workspaceId: null, statusCode: 202, durationUs: 4_000, receivedAt, attemptOutcome: "completed", attemptErrorCode: null, sessionId: "chat", messageId: "msg_2" },
      { id: "req_1", traceId: "t1", workspaceId: null, statusCode: 202, durationUs: null, receivedAt, attemptOutcome: "failed", attemptErrorCode: "model_timeout", sessionId: "chat", messageId: "msg_1" },
    ],
    recentFailures: [{ id: "req_1", traceId: "t1", workspaceId: null, statusCode: 202, durationUs: null, receivedAt, attemptOutcome: "failed", attemptErrorCode: "model_timeout", sessionId: "chat", messageId: "msg_1" }],
  }, true, 72)).toEqual({
    method: "POST", path: "/chat", declared: true,
    http: { requests: 5, status_counts: [{ status: 202, requests: 4 }, { status: 400, requests: 1 }], p50_duration_us: 12_000, p95_duration_us: 27_000, first_seen_at: "2026-09-21T12:00:00.000Z", last_seen_at: "2026-09-21T12:00:00.000Z" },
    downstream: { messages: 4, attempts: 5, completed: 2, failed: 2, pending: 1, p50_time_to_start_us: 1_500_000, error_codes: [{ code: "model_timeout", attempts: 2 }] },
    recent_requests: [
      { id: "req_2", trace_id: "t2", status_code: 202, duration_us: 4_000, received_at: "2026-09-21T12:00:00.000Z", attempt_outcome: "completed", session_id: "chat", message_id: "msg_2" },
      { id: "req_1", trace_id: "t1", status_code: 202, received_at: "2026-09-21T12:00:00.000Z", attempt_outcome: "failed", attempt_error_code: "model_timeout", session_id: "chat", message_id: "msg_1" },
    ],
    recent_failures: [{ id: "req_1", trace_id: "t1", status_code: 202, received_at: "2026-09-21T12:00:00.000Z", attempt_outcome: "failed", attempt_error_code: "model_timeout", session_id: "chat", message_id: "msg_1" }],
    traffic_window_hours: 72,
  });
});

test("an unseen route has empty measurements", () => {
  expect(appRouteDetail({
    method: "GET", path: "/never",
    http: { requests: 0, statusCounts: [], p50DurationUs: null, p95DurationUs: null, firstSeenAt: null, lastSeenAt: null },
    downstream: { messages: 0, attempts: 0, completed: 0, failed: 0, pending: 0, p50TimeToStartUs: null, p50TimeToFinishUs: null, errorCodes: [] },
    recentRequests: [],
    recentFailures: [],
  }, false, 72)).toEqual({
    method: "GET", path: "/never", declared: false,
    http: { requests: 0, status_counts: [] },
    downstream: { messages: 0, attempts: 0, completed: 0, failed: 0, pending: 0, error_codes: [] },
    recent_requests: [],
    recent_failures: [],
    traffic_window_hours: 72,
  });
});
