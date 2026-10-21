import { expect, test } from "bun:test";

import { mergeRequests } from "../lib/request-merge.ts";

const event = (overrides = {}) => ({
  sequence: 10,
  event_id: "evt_10",
  request_id: "req_10",
  workspace_id: "wsp_production",
  session_id: "ses_1",
  received_at: "2026-08-13T00:00:10.000Z",
  status_code: 200,
  duration_ms: 42,
  ...overrides,
});

const invocation = (overrides = {}) => ({
  request_id: "req_10",
  timestamp: "2026-08-13T00:00:10.000Z",
  method: "POST",
  path: "/v1/messages",
  status: 200,
  duration_ms: 48,
  logs: [{ timestamp: "2026-08-13T00:00:10.100Z", level: "log", message: "dispatch" }],
  ...overrides,
});

test("joins the edge and gateway legs of one request into a single row", () => {
  const [row, ...rest] = mergeRequests([event()], [invocation()]);
  expect(rest).toEqual([]);
  expect(row.key).toBe("req_10");
  expect(row.workspaceID).toBe("wsp_production");
  expect(row.sessionID).toBe("ses_1");
  expect(row.method).toBe("POST");
  expect(row.path).toBe("/v1/messages");
  expect(row.logs).toHaveLength(1);
});

test("takes status and duration from the gateway, keeping the edge's own", () => {
  const [row] = mergeRequests(
    [event({ status_code: 503, duration_ms: 8_000 })],
    [invocation({ status: 502, duration_ms: 8_010 })],
  );
  expect(row.status).toBe(503);
  expect(row.durationMS).toBe(8_000);
  expect(row.edgeStatus).toBe(502);
  expect(row.edgeDurationMS).toBe(8_010);
});

test("keeps an edge invocation that never reached a workspace", () => {
  const rows = mergeRequests([], [invocation({ request_id: "req_edge", status: 404, duration_ms: 1 })]);
  expect(rows).toHaveLength(1);
  expect(rows[0].workspaceID).toBeUndefined();
  expect(rows[0].sequence).toBeUndefined();
  expect(rows[0].status).toBe(404);
  expect(rows[0].edgeStatus).toBe(404);
});

test("leaves a gateway event recorded before the request ID upgrade uncorrelated", () => {
  const { request_id: _dropped, ...withoutRequestID } = event();
  const [row] = mergeRequests([withoutRequestID], [invocation()]);
  expect(row.key).toBe("evt_10");
  expect(row.requestID).toBeUndefined();
  expect(row.method).toBeUndefined();
  expect(row.logs).toEqual([]);
});

test("an edge invocation is claimed by at most one gateway event", () => {
  const rows = mergeRequests(
    [event({ sequence: 11, event_id: "evt_11", received_at: "2026-08-13T00:00:11.000Z" })],
    [invocation()],
  );
  // The invocation matched the event, so it must not also appear on its own.
  expect(rows).toHaveLength(1);
  expect(rows[0].eventID).toBe("evt_11");
});

test("orders newest first, breaking same-millisecond ties by gateway sequence", () => {
  const rows = mergeRequests(
    [
      event({ sequence: 1, event_id: "evt_1", request_id: "req_1", received_at: "2026-08-13T00:00:01.000Z" }),
      event({ sequence: 3, event_id: "evt_3", request_id: "req_3", received_at: "2026-08-13T00:00:09.000Z" }),
      event({ sequence: 2, event_id: "evt_2", request_id: "req_2", received_at: "2026-08-13T00:00:09.000Z" }),
    ],
    [],
  );
  expect(rows.map(({ sequence }) => sequence)).toEqual([3, 2, 1]);
});
