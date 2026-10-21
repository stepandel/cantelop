import { expect, test } from "bun:test";

import { parseObservationBatch } from "./events";

const base = {
  event_id: "dispatcher:attempt:1",
  schema_version: 1,
  source: "fire_fuse",
  occurred_at: "2026-09-01T12:00:00.000Z",
  app_id: "app_0123456789abcdef0123456789abcdef",
  workspace_id: "wsp_0123456789abcdef0123456789abcdef",
  logical_session_id: "agent:primary",
  message_id: "msg_0123456789abcdef0123456789abcdef",
  request_id: "req_abcdefabcdefabcdefabcdefabcdefab",
  accepted_at: "2026-09-01T11:59:59.000Z",
};

test("parses typed Message attempt observations", () => {
  const batch = parseObservationBatch({ events: [{
    ...base,
    type: "attempt.started",
    attempt_id: "att_0123456789abcdef0123456789abcdef",
    attempt: 1,
    trace_id: "abcdefabcdefabcdefabcdefabcdefab",
    span_id: "0123456789abcdef",
    queued_at: "2026-09-01T12:00:00.000Z",
  }] });
  expect(batch.events[0]).toMatchObject({ type: "attempt.started", attempt: 1 });
});

test("rejects forged and unbounded observation batches", () => {
  expect(() => parseObservationBatch({ events: [] })).toThrow();
  expect(() => parseObservationBatch({ events: Array.from({ length: 257 }, () => base) })).toThrow();
  expect(() => parseObservationBatch({ events: [{ ...base, type: "message.accepted", app_id: "app_other" }] })).toThrow("app_id is invalid");
  expect(() => parseObservationBatch({ events: [{
    ...base,
    type: "attempt.started",
    attempt_id: "att_0123456789abcdef0123456789abcdef",
    attempt: 1,
    trace_id: "00000000000000000000000000000000",
    span_id: "0123456789abcdef",
    queued_at: "2026-09-01T12:00:00.000Z",
  }] })).toThrow("trace_id is invalid");
});

test("parses SDK spans and logs without trusting runtime scope", () => {
  const sdkBase = {
    event_id: "1111111111111111:span.started",
    schema_version: 1,
    source: "sdk",
    occurred_at: "2026-09-01T12:00:00.000Z",
    app_id: base.app_id,
    attempt_id: "att_0123456789abcdef0123456789abcdef",
    trace_id: "abcdefabcdefabcdefabcdefabcdefab",
  };
  const batch = parseObservationBatch({ events: [{
    ...sdkBase,
    type: "span.started",
    span_id: "1111111111111111",
    name: "session.receive",
    kind: "internal",
    started_at: sdkBase.occurred_at,
    attributes: { model: "test" },
  }, {
    ...sdkBase,
    event_id: "log_22222222222222222222222222222222",
    type: "log.recorded",
    log_id: "log_22222222222222222222222222222222",
    span_id: "1111111111111111",
    severity: "info",
    body: "handler entered",
    attributes: {},
  }] });
  expect(batch.events.map(({ type }) => type)).toEqual(["span.started", "log.recorded"]);
});

test("parses sandbox-scoped runtime output without inventing an attempt", () => {
  const event = parseObservationBatch({ events: [{
    event_id: "log_33333333333333333333333333333333",
    schema_version: 1,
    source: "sdk",
    type: "log.recorded",
    occurred_at: "2026-09-01T12:00:00.000Z",
    app_id: base.app_id,
    workspace_id: base.workspace_id,
    logical_session_id: base.logical_session_id,
    sandbox_id: "sbx-0123456789abcdef0123456789abcdef",
    concrete_session_id: "ses_actor",
    log_id: "log_33333333333333333333333333333333",
    severity: "error",
    body: "startup failed",
    attributes: { source: "stderr", automatic: true },
  }] }).events[0];
  expect(event).toMatchObject({ type: "log.recorded", sandbox_id: "sbx-0123456789abcdef0123456789abcdef" });
  expect(event).not.toHaveProperty("attempt_id");
});

test("parses request-scoped edge output without Message or Sandbox scope", () => {
  const event = parseObservationBatch({ events: [{
    event_id: "edge:req:log:1",
    schema_version: 1,
    source: "edge",
    type: "log.recorded",
    occurred_at: "2026-09-01T12:00:00.000Z",
    app_id: base.app_id,
    request_id: base.request_id,
    trace_id: "abcdefabcdefabcdefabcdefabcdefab",
    log_id: "log_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    severity: "info",
    body: "edge handler entered",
    attributes: { surface: "edge", source: "console.log" },
  }] }).events[0];
  expect(event).toMatchObject({ type: "log.recorded", request_id: base.request_id });
  expect(event).not.toHaveProperty("attempt_id");
});
