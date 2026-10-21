import { afterAll, beforeAll, expect, test } from "bun:test";
import pg from "pg";

import { observabilityDatabaseConfig } from "./database";
import { ObservationRepository } from "./repository";

// Route aggregation over hand-seeded rows in the real schema: HTTP outcomes,
// the messages each request dispatched, attempt outcomes and timings, and the
// retention rule that keeps a request while a message still references it.
const databaseURL = process.env.CANTELOP_TEST_DATABASE_URL;
const postgresTest = databaseURL ? test : test.skip;

const appID = "app_00000000000000000000000000000abc";
const workspaceID = "wsp_00000000000000000000000000000abc";
const hex = (n) => n.toString(16).padStart(32, "0");
const at = (seconds) => new Date(Date.UTC(2026, 8, 21, 12, 0, seconds));

let pool;
let repository;

beforeAll(async () => {
  if (!databaseURL) return;
  const sslMode = ["localhost", "127.0.0.1", "::1"].includes(new URL(databaseURL).hostname) ? "disable" : "verify-full";
  pool = new pg.Pool(observabilityDatabaseConfig({ CANTELOP_DATABASE_URL: databaseURL, CANTELOP_DATABASE_SSL: sslMode }));
  repository = ObservationRepository.fromPool(pool);
  const client = await pool.connect();
  try {
    await client.query("BEGIN");
    await client.query("DELETE FROM cantelop_observability.sessions WHERE app_id = $1", [appID]);
    await client.query("DELETE FROM cantelop_observability.requests WHERE app_id = $1", [appID]);
    const session = (await client.query(
      `INSERT INTO cantelop_observability.sessions (app_id, workspace_id, logical_session_id, first_seen_at, last_seen_at)
       VALUES ($1, $2, 'route-detail', $3, $3) RETURNING id`, [appID, workspaceID, at(0)])).rows[0].id;
    // requests: [id, method, path, status, duration_us, received]
    const requests = [
      [1, "POST", "/chat", 202, 12_000, 0],
      [2, "POST", "/chat/", 202, 30_000, 10],   // trailing slash normalizes onto /chat
      [3, "post", "/chat", 202, 8_000, 20],     // lowercase method normalizes too
      [4, "POST", "/chat", 400, 2_000, 30],     // rejected at the edge, nothing dispatched
      [5, "POST", "/chat", 202, 15_000, 40],    // dispatched, attempt still pending
      [6, "GET", "/health", 200, 500, 50],
      [7, "GET", "/health", 200, null, 60],
    ];
    for (const [n, method, path, status, duration, seconds] of requests) {
      await client.query(
        `INSERT INTO cantelop_observability.requests (id, app_id, workspace_id, trace_id, method, path, status_code, duration_us, received_at, completed_at)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`,
        [`req_${hex(n)}`, appID, workspaceID, hex(1000 + n), method, path, status, duration, at(seconds)]);
    }
    // messages: [n, request n, accepted seconds]
    const messages = [[1, 1, 0], [2, 2, 10], [3, 3, 20], [4, 5, 40]];
    for (const [n, request, seconds] of messages) {
      await client.query(
        `INSERT INTO cantelop_observability.messages (id, session_id, app_id, workspace_id, request_id, accepted_at)
         VALUES ($1, $2, $3, $4, $5, $6)`, [`msg_${hex(n)}`, session, appID, workspaceID, `req_${hex(request)}`, at(seconds)]);
    }
    // attempts: [n, message n, attempt, outcome, error, started offset s, finished offset s]
    const attempts = [
      [1, 1, 1, "completed", null, 1, 5],
      [2, 2, 1, "failed", "model_timeout", 2, 12],
      [3, 2, 2, "completed", null, 14, 20],   // retry succeeded: message 2 counts once, attempts twice
      [4, 3, 1, "failed", "model_timeout", 1, 3],
      [5, 4, 1, "pending", null, null, null],
    ];
    for (const [n, message, attempt, outcome, error, start, finish] of attempts) {
      const accepted = at(messages.find(([m]) => m === message)[2]);
      const offset = (s) => (s === null ? null : new Date(accepted.valueOf() + s * 1_000));
      await client.query(
        `INSERT INTO cantelop_observability.message_attempts
           (id, message_id, app_id, attempt, trace_id, span_id, queued_at, handler_started_at, finished_at, outcome, error_code)
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
        [`att_${hex(n)}`, `msg_${hex(message)}`, appID, attempt, hex(2000 + n), hex(3000 + n).slice(16), accepted, offset(start), offset(finish), outcome, error]);
    }
    await client.query("COMMIT");
  } catch (error) {
    await client.query("ROLLBACK");
    throw error;
  } finally {
    client.release();
  }
});

afterAll(async () => { await pool?.end(); });

postgresTest("routeTraffic joins each normalized route with the messages it dispatched", async () => {
  const traffic = await repository.routeTraffic(appID, [{ method: "GET", path: "/health" }]);
  expect(traffic.map(({ method, path, requests, clientErrors, messages, failedAttempts }) => ({ method, path, requests, clientErrors, messages, failedAttempts })))
    .toEqual([
      { method: "GET", path: "/health", requests: 2, clientErrors: 0, messages: 0, failedAttempts: 0 },
      { method: "POST", path: "/chat", requests: 5, clientErrors: 1, messages: 4, failedAttempts: 2 },
    ]);
});

postgresTest("routeDetail reports HTTP outcomes, downstream attempt outcomes, timings, and recent failures", async () => {
  const detail = await repository.routeDetail(appID, "POST", "/chat");
  expect(detail.http).toEqual({
    requests: 5,
    statusCounts: [{ status: 202, requests: 4 }, { status: 400, requests: 1 }],
    p50DurationUs: 12_000,
    p95DurationUs: 27_000,
    firstSeenAt: at(0),
    lastSeenAt: at(40),
  });
  expect(detail.downstream).toEqual({
    messages: 4,
    attempts: 5,
    completed: 2,
    failed: 2,
    pending: 1,
    p50TimeToStartUs: 1_500_000,
    p50TimeToFinishUs: 12_500_000,
    errorCodes: [{ code: "model_timeout", attempts: 2 }],
  });
  // Newest first: the 400 and the two requests whose work failed. Request 2
  // recovered on retry but is still listed, with its failed attempt shown.
  expect(detail.recentFailures.map(({ id, statusCode, attemptOutcome, attemptErrorCode }) => ({ id, statusCode, attemptOutcome, attemptErrorCode })))
    .toEqual([
      { id: `req_${hex(4)}`, statusCode: 400, attemptOutcome: null, attemptErrorCode: null },
      { id: `req_${hex(3)}`, statusCode: 202, attemptOutcome: "failed", attemptErrorCode: "model_timeout" },
      { id: `req_${hex(2)}`, statusCode: 202, attemptOutcome: "failed", attemptErrorCode: "model_timeout" },
    ]);
  expect(detail.recentFailures[1].traceId).toBe(hex(1003));
  // bigint columns arrive from pg as strings unless cast; the wire must carry numbers.
  expect(detail.recentFailures.map(({ durationUs }) => typeof durationUs)).toEqual(["number", "number", "number"]);
  expect(detail.recentFailures[0].durationUs).toBe(2_000);
  // Recent requests include the ones that succeeded, each with the Session it reached.
  expect(detail.recentRequests.length).toBeGreaterThan(detail.recentFailures.length);
  expect(detail.recentRequests.filter(({ messageId }) => messageId !== null).every(({ sessionId }) => typeof sessionId === "string")).toBe(true);

  const health = await repository.routeDetail(appID, "GET", "/health");
  expect(health.http.requests).toBe(2);
  expect(health.http.p50DurationUs).toBe(500);
  expect(health.downstream).toMatchObject({ messages: 0, attempts: 0, errorCodes: [] });
  expect(health.recentFailures).toEqual([]);

  const unknown = await repository.routeDetail(appID, "DELETE", "/nothing");
  expect(unknown.http).toEqual({ requests: 0, statusCounts: [], p50DurationUs: null, p95DurationUs: null, firstSeenAt: null, lastSeenAt: null });
});

postgresTest("requestTrace returns one request with its messages and attempts, or nothing", async () => {
  const trace = await repository.requestTrace(appID, `req_${hex(2)}`);
  expect(trace.requests).toHaveLength(1);
  expect(trace.requests[0].id).toBe(`req_${hex(2)}`);
  expect(trace.requests[0].messages).toHaveLength(1);
  expect(trace.requests[0].messages[0].attempts.map(({ outcome }) => outcome)).toEqual(["failed", "completed"]);
  expect(trace.requests[0].duration_us).toBe(30_000);
  expect(trace.unlinked_messages).toEqual([]);
  const missing = await repository.requestTrace(appID, `req_${hex(999)}`);
  expect(missing).toEqual({ app_id: appID, requests: [], unlinked_messages: [] });
});

postgresTest("retention keeps a request while a pending message references it", async () => {
  const client = await pool.connect();
  try {
    // Force the sweep with a cutoff after every seeded row.
    await client.query("BEGIN");
    await client.query(`INSERT INTO cantelop_observability.retention_state (id, last_run_at) VALUES (1, '2000-01-01')
      ON CONFLICT (id) DO UPDATE SET last_run_at = '2000-01-01'`);
    await client.query("COMMIT");
  } finally { client.release(); }
  const sweep = new ObservationRepository(pool, 24);
  expect(await sweep.expireIfDue(new Date(at(0).valueOf() + 25 * 3_600_000))).toBe(true);
  const survivors = await pool.query(
    "SELECT id FROM cantelop_observability.requests WHERE app_id = $1 ORDER BY id", [appID]);
  // Only the request behind the pending attempt survives, together with its message.
  expect(survivors.rows.map(({ id }) => id)).toEqual([`req_${hex(5)}`]);
  const messages = await pool.query("SELECT id, request_id FROM cantelop_observability.messages WHERE app_id = $1", [appID]);
  expect(messages.rows).toEqual([{ id: `msg_${hex(4)}`, request_id: `req_${hex(5)}` }]);
});
