import { expect, test } from "bun:test";

import { buildAppTraceTree } from "./trace-tree";

const appID = "app_0123456789abcdef0123456789abcdef";

function row(overrides = {}) {
  return {
    root_kind: "request",
    root_id: "req_abcdefabcdefabcdefabcdefabcdefab",
    root_at: "2026-09-01T12:00:00Z",
    request_id: "req_abcdefabcdefabcdefabcdefabcdefab",
    request_method: "POST",
    request_path: "/messages",
    request_status_code: 202,
    request_duration_us: 1200,
    request_outcome: "completed",
    request_received_at: "2026-09-01T12:00:00Z",
    request_completed_at: "2026-09-01T12:00:00.001Z",
    request_logs: [{
      id: "log_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      severity: "info",
      body: "edge handler entered",
      occurred_at: "2026-09-01T12:00:00.000Z",
      attributes: { surface: "edge" },
    }],
    message_id: "msg_0123456789abcdef0123456789abcdef",
    message_accepted_at: "2026-09-01T12:00:01Z",
    session_id: "session-db-id",
    workspace_id: "wsp_0123456789abcdef0123456789abcdef",
    logical_session_id: "agent:primary",
    session_first_seen_at: "2026-09-01T12:00:00Z",
    session_last_seen_at: "2026-09-01T12:01:00Z",
    sandbox_id: "sbx-0123456789abcdef0123456789abcdef",
    concrete_session_id: "ses_actor",
    build_id: "build-1",
    sandbox_allocated_at: "2026-09-01T12:00:00Z",
    sandbox_logs: [{
      id: "log_44444444444444444444444444444444",
      severity: "error",
      body: JSON.stringify({ component: "cantelop.sdk", event: "message_lifecycle", state: "handled" }),
    }],
    attempt_id: "att_0123456789abcdef0123456789abcdef",
    attempt_number: 1,
    trace_id: "abcdefabcdefabcdefabcdefabcdefab",
    attempt_span_id: "0123456789abcdef",
    attempt_outcome: "failed",
    attempt_error_code: "delivery_attempt_failed",
    attempt_queued_at: "2026-09-01T12:00:01Z",
    attempt_routed_at: "2026-09-01T12:00:01.100Z",
    attempt_finished_at: "2026-09-01T12:00:02Z",
    spans: [{ id: "1111111111111111", name: "session.receive" }],
    logs: [{ id: "log_22222222222222222222222222222222", body: "entered" }],
    ...overrides,
  };
}

test("reconstructs Request Message Attempt Sandbox Session trees with retries", () => {
  const tree = buildAppTraceTree(appID, [row(), row({
    attempt_id: "att_33333333333333333333333333333333",
    attempt_number: 2,
    attempt_outcome: "completed",
    attempt_error_code: null,
  })]);
  expect(tree.app_id).toBe(appID);
  expect(tree.requests).toHaveLength(1);
  expect(tree.requests[0].messages).toHaveLength(1);
  expect(tree.requests[0].logs[0].body).toBe("edge handler entered");
  expect(tree.requests[0].messages[0].attempts.map(({ number }) => number)).toEqual([1, 2]);
  expect(tree.requests[0].messages[0].attempts[0].sandbox.logs[0].severity).toBe("info");
  expect(tree.requests[0].messages[0].session.logical_session_id).toBe("agent:primary");
});

test("keeps requests without messages as roots", () => {
  const tree = buildAppTraceTree(appID, [row({
    message_id: null,
    message_accepted_at: null,
    session_id: null,
    workspace_id: null,
    logical_session_id: null,
    session_first_seen_at: null,
    session_last_seen_at: null,
    sandbox_id: null,
    concrete_session_id: null,
    build_id: null,
    sandbox_allocated_at: null,
    attempt_id: null,
    attempt_number: null,
    trace_id: null,
    attempt_span_id: null,
    attempt_outcome: null,
    attempt_error_code: null,
    attempt_queued_at: null,
    attempt_routed_at: null,
    attempt_finished_at: null,
  })]);
  expect(tree.requests[0].messages).toEqual([]);
});

test("shows messages whose request observation has not arrived separately", () => {
  const tree = buildAppTraceTree(appID, [row({
    root_kind: "message",
    root_id: "msg_0123456789abcdef0123456789abcdef",
    root_at: "2026-09-01T12:00:01Z",
    request_id: null,
    request_method: null,
    request_path: null,
    request_status_code: null,
    request_duration_us: null,
    request_outcome: null,
    request_received_at: null,
    request_completed_at: null,
  })]);
  expect(tree.requests).toEqual([]);
  expect(tree.unlinked_messages[0].id).toStartWith("msg_");
});
