import { expect, test } from "bun:test";

import { ObservationRejectedError, ObservationRepository } from "./repository";

class FakeTransaction {
  queries = [];
  duplicate = false;
  retentionDue = false;

  async query(sql, values = []) {
    this.queries.push({ sql: sql.trim(), values });
    if (sql.includes("ingestion_receipts")) {
      return this.duplicate ? { rows: [], rowCount: 0 } : { rows: [{ event_id: values[1] }], rowCount: 1 };
    }
    if (sql.includes("retention_state")) {
      return this.retentionDue ? { rows: [{ id: 1 }], rowCount: 1 } : { rows: [], rowCount: 0 };
    }
    if (sql.includes("RETURNING id")) return { rows: [{ id: "00000000-0000-4000-8000-000000000001" }], rowCount: 1 };
    return { rows: [], rowCount: 1 };
  }

  release() {}
}

const accepted = {
  event_id: "gateway:message:accepted",
  schema_version: 1,
  source: "gateway",
  type: "message.accepted",
  occurred_at: "2026-09-01T12:00:00Z",
  app_id: "app_0123456789abcdef0123456789abcdef",
  workspace_id: "wsp_0123456789abcdef0123456789abcdef",
  logical_session_id: "agent:primary",
  message_id: "msg_0123456789abcdef0123456789abcdef",
  request_id: "req_abcdefabcdefabcdefabcdefabcdefab",
  accepted_at: "2026-09-01T12:00:00Z",
};

test("ingests typed observations atomically", async () => {
  const transaction = new FakeTransaction();
  const repository = new ObservationRepository({ connect: async () => transaction });
  expect(await repository.ingest({ events: [accepted] })).toEqual({ accepted: 1, duplicated: 0 });
  expect(transaction.queries[0].sql).toBe("BEGIN");
  expect(transaction.queries.some(({ sql }) => sql.includes("cantelop_observability.sessions"))).toBe(true);
  expect(transaction.queries.some(({ sql }) => sql.includes("cantelop_observability.messages"))).toBe(true);
  expect(transaction.queries.at(-1).sql).toBe("COMMIT");
});

test("deduplicates retried source events before applying them", async () => {
  const transaction = new FakeTransaction();
  transaction.duplicate = true;
  const repository = new ObservationRepository({ connect: async () => transaction });
  expect(await repository.ingest({ events: [accepted] })).toEqual({ accepted: 0, duplicated: 1 });
  expect(transaction.queries.some(({ sql }) => sql.includes("cantelop_observability.sessions"))).toBe(false);
});

test("rejects impossible SDK references without classifying database outages as bad events", async () => {
  const event = {
    ...accepted, source: "sdk", type: "span.started", event_id: "missing:span.started",
    attempt_id: "att_0123456789abcdef0123456789abcdef", trace_id: "abcdefabcdefabcdefabcdefabcdefab",
    span_id: "1111111111111111", name: "session.receive", kind: "internal", started_at: accepted.occurred_at, attributes: {},
  };
  for (const unavailable of [false, true]) {
    const transaction = new FakeTransaction();
    const original = transaction.query.bind(transaction);
    const outage = new Error("connection lost");
    transaction.query = async (sql, values) => {
      if (sql.includes("INSERT INTO cantelop_observability.spans")) {
        if (unavailable) throw outage;
        return { rows: [], rowCount: 0 };
      }
      return original(sql, values);
    };
    const repository = new ObservationRepository({ connect: async () => transaction });
    if (unavailable) {
      await expect(repository.ingest({ events: [event] })).rejects.toBe(outage);
    } else {
      await expect(repository.ingest({ events: [event] })).rejects.toBeInstanceOf(ObservationRejectedError);
    }
    expect(transaction.queries.at(-1).sql).toBe("ROLLBACK");
  }
});

test("classifies PostgreSQL identity constraints as rejected batches", async () => {
  for (const code of ["23505", "23503", "23514", "23502"]) {
    const transaction = new FakeTransaction();
    const original = transaction.query.bind(transaction);
    transaction.query = async (sql, values) => {
      if (sql.includes("INSERT INTO cantelop_observability.messages")) {
        throw Object.assign(new Error("constraint violation"), { code });
      }
      return original(sql, values);
    };
    const repository = new ObservationRepository({ connect: async () => transaction });
    await expect(repository.ingest({ events: [accepted] })).rejects.toBeInstanceOf(ObservationRejectedError);
    expect(transaction.queries.at(-1).sql).toBe("ROLLBACK");
  }
});

test("stores SDK spans before their logs and completion", async () => {
  const transaction = new FakeTransaction();
  const repository = new ObservationRepository({ connect: async () => transaction });
  const common = {
    schema_version: 1,
    source: "sdk",
    occurred_at: "2026-09-01T12:00:00Z",
    app_id: accepted.app_id,
    attempt_id: "att_0123456789abcdef0123456789abcdef",
    trace_id: "abcdefabcdefabcdefabcdefabcdefab",
  };
  const events = [{
    ...common,
    event_id: "1111111111111111:span.started",
    type: "span.started",
    span_id: "1111111111111111",
    name: "session.receive",
    kind: "internal",
    started_at: common.occurred_at,
    attributes: {},
  }, {
    ...common,
    event_id: "log_22222222222222222222222222222222",
    type: "log.recorded",
    log_id: "log_22222222222222222222222222222222",
    span_id: "1111111111111111",
    severity: "info",
    body: "handler entered",
    attributes: {},
  }, {
    ...common,
    event_id: "1111111111111111:span.completed",
    type: "span.completed",
    span_id: "1111111111111111",
    status: "ok",
    finished_at: "2026-09-01T12:00:01Z",
    attributes: {},
  }];
  expect(await repository.ingest({ events })).toEqual({ accepted: 3, duplicated: 0 });
  const statements = transaction.queries.map(({ sql }) => sql);
  expect(statements.findIndex((sql) => sql.includes("INSERT INTO cantelop_observability.spans")))
    .toBeLessThan(statements.findIndex((sql) => sql.includes("INSERT INTO cantelop_observability.logs")));
  expect(statements.some((sql) => sql.includes("UPDATE cantelop_observability.spans"))).toBe(true);
});

test("stores unscoped runtime output against its Sandbox", async () => {
  const transaction = new FakeTransaction();
  const repository = new ObservationRepository({ connect: async () => transaction });
  const event = {
    event_id: "log_33333333333333333333333333333333",
    schema_version: 1,
    source: "sdk",
    type: "log.recorded",
    occurred_at: "2026-09-01T12:00:00Z",
    app_id: accepted.app_id,
    workspace_id: accepted.workspace_id,
    logical_session_id: accepted.logical_session_id,
    sandbox_id: "sbx-0123456789abcdef0123456789abcdef",
    concrete_session_id: "ses_actor",
    log_id: "log_33333333333333333333333333333333",
    severity: "info",
    body: "runtime ready",
    attributes: { source: "stdout", automatic: true },
  };
  expect(await repository.ingest({ events: [event] })).toEqual({ accepted: 1, duplicated: 0 });
  const statements = transaction.queries.map(({ sql }) => sql);
  expect(statements.some((sql) => sql.includes("INSERT INTO cantelop_observability.sandboxes"))).toBe(true);
  expect(statements.some((sql) => sql.includes("INSERT INTO cantelop_observability.logs"))).toBe(true);
});

test("stores edge output against its Request even when completion arrives later", async () => {
  const transaction = new FakeTransaction();
  const repository = new ObservationRepository({ connect: async () => transaction });
  const event = {
    event_id: "edge:req:log:1",
    schema_version: 1,
    source: "edge",
    type: "log.recorded",
    occurred_at: "2026-09-01T12:00:00Z",
    app_id: accepted.app_id,
    request_id: "req_abcdefabcdefabcdefabcdefabcdefab",
    trace_id: "abcdefabcdefabcdefabcdefabcdefab",
    log_id: "log_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    severity: "info",
    body: "edge handler entered",
    attributes: { surface: "edge", source: "console.log" },
  };
  expect(await repository.ingest({ events: [event] })).toEqual({ accepted: 1, duplicated: 0 });
  const statements = transaction.queries.map(({ sql }) => sql);
  expect(statements.some((sql) => sql.includes("INSERT INTO cantelop_observability.requests"))).toBe(true);
  expect(statements.some((sql) => sql.includes("request_id, trace_id, severity"))).toBe(true);
});

test("queries a bounded App trace projection", async () => {
  const transaction = new FakeTransaction();
  transaction.query = async function (sql, values = []) {
    this.queries.push({ sql: sql.trim(), values });
    return { rows: [], rowCount: 0 };
  };
  const repository = new ObservationRepository({ connect: async () => transaction });
  expect(await repository.traceTree(accepted.app_id, 25)).toEqual({
    app_id: accepted.app_id, requests: [], unlinked_messages: [],
  });
  expect(transaction.queries[0].values).toEqual([accepted.app_id, 25, null]);
  expect(transaction.queries[0].sql).toContain("WITH recent_roots AS");
  expect(transaction.queries[0].sql).toContain("NOT EXISTS");
  expect(transaction.queries[0].sql).toContain("LIMIT $2");
  expect(transaction.queries[0].sql).toContain("span.attempt_id = attempt.id");
  expect(transaction.queries[0].sql).toContain("runtime_log.sandbox_id = sandbox.id");
  expect(transaction.queries[0].sql).toContain("request_log.request_id = request.id");
});

test("summarizes request traffic per normalized route, declared routes first", async () => {
  const lastSeenAt = new Date("2026-09-20T10:00:00Z");
  const transaction = new FakeTransaction();
  transaction.query = async function (sql, values = []) {
    this.queries.push({ sql: sql.trim(), values });
    return {
      rowCount: 1,
      rows: [{
        method: "POST", path: "/chat", requests: 12, client_errors: 2, server_errors: 1,
        median_duration_us: 1500.5, last_seen_at: lastSeenAt, messages: 9, failed_attempts: 3,
      }],
    };
  };
  const repository = new ObservationRepository({ connect: async () => transaction });
  expect(await repository.routeTraffic(accepted.app_id, [{ method: "POST", path: "/chat" }], 50)).toEqual([{
    method: "POST", path: "/chat", requests: 12, clientErrors: 2, serverErrors: 1,
    medianDurationUs: 1501, lastSeenAt, messages: 9, failedAttempts: 3,
  }]);
  expect(transaction.queries[0].values).toEqual([accepted.app_id, ["POST /chat"], 50]);
  expect(transaction.queries[0].sql).toContain("left(request.path, -1)");
  expect(transaction.queries[0].sql).toContain("= ANY($2::text[])) DESC");
  await expect(repository.routeTraffic("app_invalid")).rejects.toThrow("invalid App route traffic query");
  await expect(repository.routeTraffic(accepted.app_id, [], 501)).rejects.toThrow("invalid App route traffic query");
  expect(repository.retentionWindowHours).toBe(72);
});

test("requestTrace scopes the tree to one request", async () => {
  const transaction = new FakeTransaction();
  transaction.query = async function (sql, values = []) {
    this.queries.push({ sql: sql.trim(), values });
    return { rows: [], rowCount: 0 };
  };
  const repository = new ObservationRepository({ connect: async () => transaction });
  const requestID = `req_${"c".repeat(32)}`;
  expect(await repository.requestTrace(accepted.app_id, requestID)).toEqual({ app_id: accepted.app_id, requests: [], unlinked_messages: [] });
  expect(transaction.queries[0].values).toEqual([accepted.app_id, 1, requestID]);
  expect(transaction.queries[0].sql).toContain("request.id = $3");
  await expect(repository.requestTrace(accepted.app_id, "req_nope")).rejects.toThrow("invalid App trace query");
});

test("routeDetail validates its query and shapes an empty route", async () => {
  const transaction = new FakeTransaction();
  transaction.query = async function (sql, values = []) {
    this.queries.push({ sql: sql.trim(), values });
    if (sql.includes("AS status_counts")) return { rowCount: 1, rows: [{ requests: 0, status_counts: null, p50_duration_us: null, p95_duration_us: null, first_seen_at: null, last_seen_at: null }] };
    if (sql.includes("AS error_codes")) return { rowCount: 1, rows: [{ messages: 0, attempts: 0, completed: 0, failed: 0, pending: 0, p50_time_to_start_us: null, p50_time_to_finish_us: null, error_codes: null }] };
    return { rowCount: 0, rows: [] };
  };
  const repository = new ObservationRepository({ connect: async () => transaction });
  expect(await repository.routeDetail(accepted.app_id, "POST", "/chat", 5)).toEqual({
    method: "POST", path: "/chat",
    http: { requests: 0, statusCounts: [], p50DurationUs: null, p95DurationUs: null, firstSeenAt: null, lastSeenAt: null },
    downstream: { messages: 0, attempts: 0, completed: 0, failed: 0, pending: 0, p50TimeToStartUs: null, p50TimeToFinishUs: null, errorCodes: [] },
    recentRequests: [],
    recentFailures: [],
  });
  expect(transaction.queries).toHaveLength(4);
  expect(transaction.queries.every(({ values }) => values.slice(0, 3).join() === `${accepted.app_id},POST,/chat`)).toBe(true);
  expect(transaction.queries[2].values[3]).toBe(5);
  // Recent requests are listed whatever their outcome; the failures query filters.
  expect(transaction.queries[2].sql).not.toContain("status_code >= 400");
  expect(transaction.queries[3].sql).toContain("status_code >= 400");
  for (const [method, path, limit] of [["post", "/chat", 5], ["POST", "chat", 5], ["POST", "/chat", 0], ["POST", "/chat", 101]]) {
    await expect(repository.routeDetail(accepted.app_id, method, path, limit)).rejects.toThrow("invalid App route detail query");
  }
});

test("lists Apps with recent trace activity for the operator view", async () => {
  const transaction = new FakeTransaction();
  transaction.query = async function (sql, values = []) {
    this.queries.push({ sql: sql.trim(), values });
    return { rows: [{ app_id: accepted.app_id }], rowCount: 1 };
  };
  const repository = new ObservationRepository({ connect: async () => transaction });
  expect(await repository.traceAppIDs(25)).toEqual([accepted.app_id]);
  expect(transaction.queries[0].values).toEqual([25]);
  expect(transaction.queries[0].sql).toContain("cantelop_observability.requests");
  expect(transaction.queries[0].sql).toContain("cantelop_observability.messages");
  expect(transaction.queries[0].sql).toContain("ORDER BY MAX(observed_at) DESC");
});

test("expires completed observability data after the configured TTL while protecting active attempts", async () => {
  const transaction = new FakeTransaction();
  transaction.retentionDue = true;
  const repository = new ObservationRepository({ connect: async () => transaction }, 72);
  const now = new Date("2026-09-04T12:00:00Z");
  // Ingestion never sweeps inside its own transaction.
  await repository.ingest({ events: [accepted] }, now);
  expect(transaction.queries.some(({ sql }) => sql.startsWith("DELETE") || sql.includes("retention_state"))).toBe(false);
  transaction.queries = [];
  expect(await repository.expireIfDue(now)).toBe(true);
  expect(transaction.queries.some(({ sql }) => sql === "BEGIN")).toBe(false);
  const deletions = transaction.queries.filter(({ sql }) => sql.startsWith("DELETE"));
  expect(deletions.length).toBeGreaterThanOrEqual(7);
  expect(deletions.every(({ values }) => values[0].toISOString() === "2026-09-01T12:00:00.000Z")).toBe(true);
  expect(deletions.every(({ sql, values }) => sql.includes("LIMIT $2") && values[1] === 5_000)).toBe(true);
  const messages = deletions.find(({ sql }) => sql.includes("observability.messages message"));
  expect(messages.sql).toContain("attempt.outcome = 'pending'");
  const requests = deletions.find(({ sql }) => sql.includes("observability.requests request"));
  expect(requests.sql).toContain("message.request_id = request.id");
  const spans = deletions.find(({ sql }) => sql.includes("observability.spans span"));
  expect(spans.sql).toContain("attempt.outcome <> 'pending'");
});

test("retention deletes in bounded batches and re-arms when its budget runs out", async () => {
  const transaction = new FakeTransaction();
  transaction.retentionDue = true;
  const original = transaction.query.bind(transaction);
  transaction.query = async (sql, values) => {
    const result = await original(sql, values);
    // An unbounded log backlog: every batch comes back full.
    return sql.includes("DELETE FROM cantelop_observability.logs") ? { rows: [], rowCount: values[1] } : result;
  };
  const repository = new ObservationRepository({ connect: async () => transaction }, 72);
  expect(await repository.expireIfDue(new Date("2026-09-04T12:00:00Z"))).toBe(true);
  const deletions = transaction.queries.filter(({ sql }) => sql.startsWith("DELETE"));
  expect(deletions.length).toBe(200);
  expect(deletions.every(({ sql }) => sql.includes("cantelop_observability.logs"))).toBe(true);
  expect(transaction.queries.at(-1).sql).toContain("SET last_run_at = last_run_at - INTERVAL '1 hour'");
});

test("retention does nothing until the previous sweep is an hour old", async () => {
  const transaction = new FakeTransaction();
  const repository = new ObservationRepository({ connect: async () => transaction }, 72);
  expect(await repository.expireIfDue(new Date("2026-09-04T12:00:00Z"))).toBe(false);
  expect(transaction.queries.filter(({ sql }) => sql.startsWith("DELETE"))).toEqual([]);
});
