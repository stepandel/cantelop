import type { Pool } from "pg";

import { withTransaction } from "../db/transaction";
import type { ObservationBatch, ObservationEvent } from "./events";
import { buildAppTraceTree, type AppTraceTree, type TraceRow } from "./trace-tree";

// A well-formed event whose references or identity cannot be accepted. Retrying
// the same ordered batch cannot repair it; transport/database errors stay retryable.
export class ObservationRejectedError extends Error {}

export interface SQLResult<Row extends Record<string, unknown> = Record<string, unknown>> {
  rows: Row[];
  rowCount: number | null;
}

export interface ObservationTransaction {
  query<Row extends Record<string, unknown> = Record<string, unknown>>(sql: string, values?: unknown[]): Promise<SQLResult<Row>>;
  release(error?: Error | boolean): void;
}

export interface ObservationPool {
  connect(): Promise<ObservationTransaction>;
}

export interface IngestionResult {
  accepted: number;
  duplicated: number;
}

type MessageEvent = Extract<ObservationEvent, {
  type: "message.accepted" | "attempt.started" | "attempt.routed" | "attempt.completed" | "attempt.failed";
}>;

export interface RouteKey {
  method: string;
  path: string;
}

export interface RouteTraffic extends RouteKey {
  requests: number;
  clientErrors: number;
  serverErrors: number;
  medianDurationUs: number | null;
  lastSeenAt: Date;
  // Messages the route's requests dispatched, and how many attempts failed.
  messages: number;
  failedAttempts: number;
}

interface RouteTrafficRow extends Record<string, unknown> {
  method: string;
  path: string;
  requests: number;
  client_errors: number;
  server_errors: number;
  median_duration_us: number | null;
  last_seen_at: Date;
  messages: number;
  failed_attempts: number;
}

export interface RouteDetail {
  method: string;
  path: string;
  http: {
    requests: number;
    statusCounts: { status: number; requests: number }[];
    p50DurationUs: number | null;
    p95DurationUs: number | null;
    firstSeenAt: Date | null;
    lastSeenAt: Date | null;
  };
  // What the route's requests dispatched into Sessions.
  downstream: {
    messages: number;
    attempts: number;
    completed: number;
    failed: number;
    pending: number;
    p50TimeToStartUs: number | null;
    p50TimeToFinishUs: number | null;
    errorCodes: { code: string; attempts: number }[];
  };
  // The latest requests whatever their outcome, then the latest that failed.
  recentRequests: RouteRecentRequest[];
  recentFailures: RouteRecentRequest[];
}

export interface RouteRecentRequest {
  id: string;
  traceId: string;
  workspaceId: string | null;
  statusCode: number | null;
  durationUs: number | null;
  receivedAt: Date;
  // The first attempt outcome and error code behind the request, if any.
  attemptOutcome: string | null;
  attemptErrorCode: string | null;
  // The Session and message the request dispatched to, if it dispatched one.
  sessionId: string | null;
  messageId: string | null;
}

interface RouteHttpRow extends Record<string, unknown> {
  requests: number;
  status_counts: { status: number; requests: number }[] | null;
  p50_duration_us: number | null;
  p95_duration_us: number | null;
  first_seen_at: Date | null;
  last_seen_at: Date | null;
}

interface RouteDownstreamRow extends Record<string, unknown> {
  messages: number;
  attempts: number;
  completed: number;
  failed: number;
  pending: number;
  p50_time_to_start_us: number | null;
  p50_time_to_finish_us: number | null;
  error_codes: { code: string; attempts: number }[] | null;
}

interface RouteFailureRow extends Record<string, unknown> {
  id: string;
  trace_id: string;
  workspace_id: string | null;
  status_code: number | null;
  duration_us: number | null;
  received_at: Date;
  attempt_outcome: string | null;
  attempt_error_code: string | null;
  session_id: string | null;
  message_id: string | null;
}

function recentRequest(row: RouteFailureRow): RouteRecentRequest {
  return {
    id: row.id,
    traceId: row.trace_id,
    workspaceId: row.workspace_id,
    statusCode: row.status_code,
    durationUs: row.duration_us === null ? null : Math.round(row.duration_us),
    receivedAt: row.received_at,
    attemptOutcome: row.attempt_outcome,
    attemptErrorCode: row.attempt_error_code,
    sessionId: row.session_id,
    messageId: row.message_id,
  };
}

// Each retention statement deletes at most $2 rows older than $1 and runs in
// its own implicit transaction; children are swept before their parents.
const RETENTION_BATCH_SIZE = 5_000;
const RETENTION_BATCH_BUDGET = 200;
const RETENTION_SWEEP = [
  `
DELETE FROM cantelop_observability.logs
WHERE id IN (
  SELECT log.id FROM cantelop_observability.logs log
  WHERE log.occurred_at < $1
  LIMIT $2
)`,
  // Active attempts retain their span structure until they settle; otherwise
  // a completion arriving after the sweep could no longer be validated.
  `
DELETE FROM cantelop_observability.spans
WHERE id IN (
  SELECT span.id FROM cantelop_observability.spans span
  JOIN cantelop_observability.message_attempts attempt ON attempt.id = span.attempt_id
  WHERE attempt.outcome <> 'pending'
    AND COALESCE(span.finished_at, span.started_at) < $1
  LIMIT $2
)`,
  `
DELETE FROM cantelop_observability.messages
WHERE id IN (
  SELECT message.id FROM cantelop_observability.messages message
  WHERE message.accepted_at < $1
    AND NOT EXISTS (
      SELECT 1 FROM cantelop_observability.message_attempts attempt
      WHERE attempt.message_id = message.id AND attempt.outcome = 'pending'
    )
  LIMIT $2
)`,
  // A message outlives the cutoff while its attempt is pending; its request
  // stays with it so the route it arrived on remains known.
  `
DELETE FROM cantelop_observability.requests
WHERE id IN (
  SELECT request.id FROM cantelop_observability.requests request
  WHERE request.received_at < $1
    AND NOT EXISTS (SELECT 1 FROM cantelop_observability.messages message WHERE message.request_id = request.id)
  LIMIT $2
)`,
  `
DELETE FROM cantelop_observability.ingestion_receipts
WHERE (source, event_id) IN (
  SELECT receipt.source, receipt.event_id FROM cantelop_observability.ingestion_receipts receipt
  WHERE receipt.received_at < $1
  LIMIT $2
)`,
  `
DELETE FROM cantelop_observability.sandboxes
WHERE id IN (
  SELECT sandbox.id FROM cantelop_observability.sandboxes sandbox
  WHERE sandbox.allocated_at < $1
    AND NOT EXISTS (SELECT 1 FROM cantelop_observability.message_attempts attempt WHERE attempt.sandbox_id = sandbox.id)
    AND NOT EXISTS (SELECT 1 FROM cantelop_observability.logs log WHERE log.sandbox_id = sandbox.id)
  LIMIT $2
)`,
  `
DELETE FROM cantelop_observability.sessions
WHERE id IN (
  SELECT session.id FROM cantelop_observability.sessions session
  WHERE session.last_seen_at < $1
    AND NOT EXISTS (SELECT 1 FROM cantelop_observability.messages message WHERE message.session_id = session.id)
    AND NOT EXISTS (SELECT 1 FROM cantelop_observability.sandboxes sandbox WHERE sandbox.session_id = session.id)
    AND NOT EXISTS (SELECT 1 FROM cantelop_observability.logs log WHERE log.session_id = session.id)
  LIMIT $2
)`,
] as const;

export class ObservationRepository {
  constructor(
    private readonly pool: ObservationPool,
    private readonly retentionHours = 72,
  ) {
    if (!Number.isSafeInteger(retentionHours) || retentionHours < 24 || retentionHours > 720) {
      throw new TypeError("invalid observability retention period");
    }
  }

  static fromPool(pool: Pool, retentionHours = 72): ObservationRepository {
    return new ObservationRepository(pool as unknown as ObservationPool, retentionHours);
  }

  async traceAppIDs(limit = 200): Promise<string[]> {
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 500) {
      throw new TypeError("invalid trace App query");
    }
    const client = await this.pool.connect();
    try {
      const result = await client.query<{ app_id: string }>(`
SELECT app_id
FROM (
  SELECT app_id, received_at AS observed_at FROM cantelop_observability.requests
  UNION ALL
  SELECT app_id, accepted_at AS observed_at FROM cantelop_observability.messages
) activity
GROUP BY app_id
ORDER BY MAX(observed_at) DESC, app_id
LIMIT $1`, [limit]);
      return result.rows.map(({ app_id: appID }) => appID);
    } finally {
      client.release();
    }
  }

  /**
   * Per-route request totals over the retention window. Paths are normalized
   * the way the SDK router matches them. Declared routes are returned first so
   * probes for paths the App never registered cannot crowd them out.
   */
  async routeTraffic(appID: string, declared: readonly RouteKey[] = [], limit = 200): Promise<RouteTraffic[]> {
    if (!/^app_[0-9a-f]{32}$/.test(appID) || !Number.isSafeInteger(limit) || limit < 1 || limit > 500) {
      throw new TypeError("invalid App route traffic query");
    }
    const client = await this.pool.connect();
    try {
      const result = await client.query<RouteTrafficRow>(`
SELECT method, path, count(*)::int AS requests,
       (count(*) FILTER (WHERE status_code BETWEEN 400 AND 499))::int AS client_errors,
       (count(*) FILTER (WHERE status_code >= 500))::int AS server_errors,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_us))::float8 AS median_duration_us,
       max(received_at) AS last_seen_at,
       (coalesce(sum(messages), 0))::int AS messages,
       (coalesce(sum(failed_attempts), 0))::int AS failed_attempts
FROM (
  SELECT upper(request.method) AS method,
         CASE WHEN length(request.path) > 1 AND right(request.path, 1) = '/'
              THEN left(request.path, -1) ELSE request.path END AS path,
         request.status_code, request.duration_us, request.received_at,
         downstream.messages, downstream.failed_attempts
  FROM cantelop_observability.requests request
  LEFT JOIN LATERAL (
    SELECT count(DISTINCT message.id)::int AS messages,
           (count(attempt.id) FILTER (WHERE attempt.outcome = 'failed'))::int AS failed_attempts
    FROM cantelop_observability.messages message
    LEFT JOIN cantelop_observability.message_attempts attempt ON attempt.message_id = message.id
    WHERE message.request_id = request.id
  ) downstream ON true
  WHERE request.app_id = $1 AND request.method IS NOT NULL AND request.path IS NOT NULL
) observed
GROUP BY method, path
ORDER BY (method || ' ' || path = ANY($2::text[])) DESC, requests DESC, path, method
LIMIT $3`, [appID, declared.map((route) => `${route.method} ${route.path}`), limit]);
      return result.rows.map((row) => ({
        method: row.method,
        path: row.path,
        requests: row.requests,
        clientErrors: row.client_errors,
        serverErrors: row.server_errors,
        medianDurationUs: row.median_duration_us === null ? null : Math.round(row.median_duration_us),
        lastSeenAt: row.last_seen_at,
        messages: row.messages,
        failedAttempts: row.failed_attempts,
      }));
    } finally {
      client.release();
    }
  }

  /**
   * One route's requests over the retention window and what they dispatched.
   * The path is matched after the same normalization as routeTraffic. Message
   * counts are taken from the message side so a request row lost to retention
   * or a failed edge publish does not hide the work it started.
   */
  async routeDetail(appID: string, method: string, path: string, failureLimit = 20): Promise<RouteDetail> {
    if (!/^app_[0-9a-f]{32}$/.test(appID) || !/^[A-Z]{1,32}$/.test(method) || !path.startsWith("/") || path.length > 2048 ||
        !Number.isSafeInteger(failureLimit) || failureLimit < 1 || failureLimit > 100) {
      throw new TypeError("invalid App route detail query");
    }
    const client = await this.pool.connect();
    try {
      const scope = `
SELECT request.id, request.trace_id, request.workspace_id, request.status_code,
       request.duration_us::float8 AS duration_us, request.received_at
FROM cantelop_observability.requests request
WHERE request.app_id = $1 AND upper(request.method) = $2
  AND CASE WHEN length(request.path) > 1 AND right(request.path, 1) = '/' THEN left(request.path, -1) ELSE request.path END = $3`;
      const http = await client.query<RouteHttpRow>(`
WITH scoped AS (${scope})
SELECT count(*)::int AS requests,
       (SELECT json_agg(json_build_object('status', status_code, 'requests', requests) ORDER BY status_code)
        FROM (SELECT status_code, count(*)::int AS requests FROM scoped WHERE status_code IS NOT NULL GROUP BY status_code) counts) AS status_counts,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_us))::float8 AS p50_duration_us,
       (percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_us))::float8 AS p95_duration_us,
       min(received_at) AS first_seen_at,
       max(received_at) AS last_seen_at
FROM scoped`, [appID, method, path]);
      const downstream = await client.query<RouteDownstreamRow>(`
WITH scoped AS (${scope}),
attempts AS (
  SELECT attempt.*, message.accepted_at
  FROM cantelop_observability.messages message
  JOIN scoped ON scoped.id = message.request_id
  LEFT JOIN cantelop_observability.message_attempts attempt ON attempt.message_id = message.id
)
SELECT (SELECT count(DISTINCT message.id)::int FROM cantelop_observability.messages message JOIN scoped ON scoped.id = message.request_id) AS messages,
       count(attempts.id)::int AS attempts,
       (count(*) FILTER (WHERE outcome = 'completed'))::int AS completed,
       (count(*) FILTER (WHERE outcome = 'failed'))::int AS failed,
       (count(*) FILTER (WHERE outcome = 'pending'))::int AS pending,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM handler_started_at - accepted_at) * 1e6)
          FILTER (WHERE handler_started_at IS NOT NULL))::float8 AS p50_time_to_start_us,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM finished_at - accepted_at) * 1e6)
          FILTER (WHERE finished_at IS NOT NULL AND outcome = 'completed'))::float8 AS p50_time_to_finish_us,
       (SELECT json_agg(json_build_object('code', error_code, 'attempts', attempts) ORDER BY attempts DESC, error_code)
        FROM (SELECT error_code, count(*)::int AS attempts FROM attempts WHERE error_code IS NOT NULL GROUP BY error_code LIMIT 10) codes) AS error_codes
FROM attempts`, [appID, method, path]);
      const recent = (failedOnly: boolean) => client.query<RouteFailureRow>(`
WITH scoped AS (${scope})
SELECT scoped.*, attempt.outcome AS attempt_outcome, attempt.error_code AS attempt_error_code,
       attempt.session_id, attempt.message_id
FROM scoped
LEFT JOIN LATERAL (
  SELECT attempt.outcome, attempt.error_code, session.logical_session_id AS session_id, message.id AS message_id
  FROM cantelop_observability.messages message
  JOIN cantelop_observability.sessions session ON session.id = message.session_id
  LEFT JOIN cantelop_observability.message_attempts attempt ON attempt.message_id = message.id
  WHERE message.request_id = scoped.id
  ORDER BY attempt.outcome = 'failed' DESC NULLS LAST, attempt.attempt DESC NULLS LAST
  LIMIT 1
) attempt ON true
${failedOnly ? "WHERE scoped.status_code >= 400 OR attempt.outcome = 'failed'" : ""}
ORDER BY scoped.received_at DESC, scoped.id DESC
LIMIT $4`, [appID, method, path, failureLimit]);
      const requests = await recent(false);
      const failures = await recent(true);
      const h = http.rows[0];
      const d = downstream.rows[0];
      const round = (value: number | null) => (value === null ? null : Math.round(value));
      return {
        method,
        path,
        http: {
          requests: h.requests,
          statusCounts: h.status_counts ?? [],
          p50DurationUs: round(h.p50_duration_us),
          p95DurationUs: round(h.p95_duration_us),
          firstSeenAt: h.first_seen_at,
          lastSeenAt: h.last_seen_at,
        },
        downstream: {
          messages: d.messages,
          attempts: d.attempts,
          completed: d.completed,
          failed: d.failed,
          pending: d.pending,
          p50TimeToStartUs: round(d.p50_time_to_start_us),
          p50TimeToFinishUs: round(d.p50_time_to_finish_us),
          errorCodes: d.error_codes ?? [],
        },
        recentRequests: requests.rows.map(recentRequest),
        recentFailures: failures.rows.map(recentRequest),
      };
    } finally {
      client.release();
    }
  }

  get retentionWindowHours(): number {
    return this.retentionHours;
  }

  /** The trace of one request, in the same shape as the App's tree. Empty
   *  requests when the edge observation has not arrived yet. */
  async requestTrace(appID: string, requestID: string): Promise<AppTraceTree> {
    if (!/^req_[0-9a-f]{32}$/.test(requestID)) throw new TypeError("invalid App trace query");
    return this.traceTree(appID, 1, requestID);
  }

  async traceTree(appID: string, rootLimit = 100, requestID: string | null = null): Promise<AppTraceTree> {
    if (!/^app_[0-9a-f]{32}$/.test(appID) || !Number.isSafeInteger(rootLimit) || rootLimit < 1 || rootLimit > 200) {
      throw new TypeError("invalid App trace query");
    }
    const client = await this.pool.connect();
    try {
      const result = await client.query<TraceRow>(`
WITH recent_roots AS (
  SELECT kind, id, observed_at FROM (
    SELECT 'request'::text AS kind, request.id, request.received_at AS observed_at
    FROM cantelop_observability.requests request
    WHERE request.app_id = $1 AND ($3::text IS NULL OR request.id = $3)
    UNION ALL
    SELECT 'message'::text AS kind, message.id, message.accepted_at AS observed_at
    FROM cantelop_observability.messages message
    WHERE message.app_id = $1 AND $3::text IS NULL AND (
      message.request_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM cantelop_observability.requests request
        WHERE request.id = message.request_id AND request.app_id = message.app_id
      )
    )
  ) activity
  ORDER BY observed_at DESC, id DESC
  LIMIT $2
)
SELECT
  root.kind AS root_kind,
  root.id AS root_id,
  root.observed_at AS root_at,
  request.id AS request_id,
  request.method AS request_method,
  request.path AS request_path,
  request.status_code AS request_status_code,
  request.duration_us::float8 AS request_duration_us,
  request.outcome AS request_outcome,
  request.received_at AS request_received_at,
  request.completed_at AS request_completed_at,
  COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', request_log.id, 'severity', request_log.severity,
      'body', request_log.body, 'occurred_at', request_log.occurred_at,
      'attributes', request_log.attributes
    ) ORDER BY request_log.occurred_at, request_log.id)
    FROM cantelop_observability.logs request_log
    WHERE request_log.request_id = request.id
  ), '[]'::jsonb) AS request_logs,
  message.id AS message_id,
  message.accepted_at AS message_accepted_at,
  session.id AS session_id,
  session.workspace_id,
  session.logical_session_id,
  session.first_seen_at AS session_first_seen_at,
  session.last_seen_at AS session_last_seen_at,
  sandbox.id AS sandbox_id,
  sandbox.concrete_session_id,
  sandbox.build_id,
  sandbox.allocated_at AS sandbox_allocated_at,
  COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', runtime_log.id, 'severity', runtime_log.severity,
      'body', runtime_log.body, 'occurred_at', runtime_log.occurred_at,
      'attributes', runtime_log.attributes
    ) ORDER BY runtime_log.occurred_at, runtime_log.id)
    FROM cantelop_observability.logs runtime_log
    WHERE runtime_log.sandbox_id = sandbox.id AND runtime_log.attempt_id IS NULL
  ), '[]'::jsonb) AS sandbox_logs,
  attempt.id AS attempt_id,
  attempt.attempt AS attempt_number,
  attempt.trace_id,
  attempt.span_id AS attempt_span_id,
  attempt.outcome AS attempt_outcome,
  attempt.error_code AS attempt_error_code,
  attempt.queued_at AS attempt_queued_at,
  attempt.routed_at AS attempt_routed_at,
  attempt.finished_at AS attempt_finished_at,
  COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', span.id, 'parent_span_id', span.parent_span_id, 'name', span.name,
      'kind', span.kind, 'status', span.status, 'started_at', span.started_at,
      'finished_at', span.finished_at, 'attributes', span.attributes
    ) ORDER BY span.started_at, span.id)
    FROM cantelop_observability.spans span WHERE span.attempt_id = attempt.id
  ), '[]'::jsonb) AS spans,
  COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', log.id, 'span_id', log.span_id, 'severity', log.severity,
      'body', log.body, 'occurred_at', log.occurred_at, 'attributes', log.attributes
    ) ORDER BY log.occurred_at, log.id)
    FROM cantelop_observability.logs log WHERE log.attempt_id = attempt.id
  ), '[]'::jsonb) AS logs
FROM recent_roots root
LEFT JOIN cantelop_observability.requests request
  ON root.kind = 'request' AND request.id = root.id AND request.app_id = $1
LEFT JOIN cantelop_observability.messages message
  ON (root.kind = 'request' AND message.request_id = request.id AND message.app_id = request.app_id)
  OR (root.kind = 'message' AND message.id = root.id AND message.app_id = $1)
LEFT JOIN cantelop_observability.sessions session ON session.id = message.session_id
LEFT JOIN cantelop_observability.message_attempts attempt ON attempt.message_id = message.id
LEFT JOIN cantelop_observability.sandboxes sandbox ON sandbox.id = attempt.sandbox_id
ORDER BY root.observed_at DESC, root.id DESC, message.accepted_at DESC NULLS LAST,
  attempt.attempt ASC`, [appID, rootLimit, requestID]);
      return buildAppTraceTree(appID, result.rows);
    } finally {
      client.release();
    }
  }

  async ingest(batch: ObservationBatch, receivedAt = new Date()): Promise<IngestionResult> {
    try {
      return await withTransaction(this.pool, async (client) => {
        let accepted = 0;
        let duplicated = 0;
        for (const event of batch.events) {
          const receipt = await client.query<{ event_id: string }>(`
INSERT INTO cantelop_observability.ingestion_receipts (source, event_id, schema_version, received_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (source, event_id) DO NOTHING
RETURNING event_id`, [event.source, event.event_id, event.schema_version, receivedAt]);
          if (receipt.rowCount === 0) {
            duplicated += 1;
            continue;
          }
          await this.apply(client, event);
          accepted += 1;
        }
        return { accepted, duplicated };
      });
    } catch (error) {
      // PostgreSQL can also reject an identity through a secondary unique key,
      // foreign key, or check constraint. These are bad batches, not outages.
      if (error && typeof error === "object" && "code" in error &&
          ["23505", "23503", "23514", "23502"].includes(String(error.code))) {
        throw new ObservationRejectedError("observation violates a storage constraint", { cause: error });
      }
      throw error;
    }
  }

  /**
   * Deletes observations older than the retention window, at most once an
   * hour. It runs after ingestion commits, never inside its transaction, and
   * deletes in bounded batches so a backlog cannot hold long row locks. A sweep
   * that exhausts its batch budget re-arms itself for the next caller.
   * Returns whether this call claimed the sweep.
   */
  async expireIfDue(now = new Date()): Promise<boolean> {
    const client = await this.pool.connect();
    try {
      const claimed = await client.query<{ id: number }>(`
INSERT INTO cantelop_observability.retention_state (id, last_run_at)
VALUES (1, $1)
ON CONFLICT (id) DO UPDATE SET last_run_at = EXCLUDED.last_run_at
WHERE cantelop_observability.retention_state.last_run_at <= EXCLUDED.last_run_at - INTERVAL '1 hour'
RETURNING id`, [now]);
      if (claimed.rowCount !== 1) return false;
      const cutoff = new Date(now.valueOf() - this.retentionHours * 60 * 60 * 1_000);
      let budget = RETENTION_BATCH_BUDGET;
      for (const statement of RETENTION_SWEEP) {
        for (;;) {
          if (budget === 0) {
            await client.query(`
UPDATE cantelop_observability.retention_state
SET last_run_at = last_run_at - INTERVAL '1 hour'
WHERE id = 1 AND last_run_at = $1`, [now]);
            return true;
          }
          budget -= 1;
          const result = await client.query(statement, [cutoff, RETENTION_BATCH_SIZE]);
          if ((result.rowCount ?? 0) < RETENTION_BATCH_SIZE) break;
        }
      }
      return true;
    } finally {
      client.release();
    }
  }

  private async apply(client: ObservationTransaction, event: ObservationEvent): Promise<void> {
    switch (event.type) {
      case "request.completed":
        await client.query(`
INSERT INTO cantelop_observability.requests
  (id, app_id, workspace_id, trace_id, method, path, status_code, duration_us, outcome, received_at, completed_at, incomplete)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false)
ON CONFLICT (id) DO UPDATE SET
  workspace_id = COALESCE(cantelop_observability.requests.workspace_id, EXCLUDED.workspace_id),
  method = COALESCE(cantelop_observability.requests.method, EXCLUDED.method),
  path = COALESCE(cantelop_observability.requests.path, EXCLUDED.path),
  status_code = COALESCE(cantelop_observability.requests.status_code, EXCLUDED.status_code),
  duration_us = COALESCE(cantelop_observability.requests.duration_us, EXCLUDED.duration_us),
  outcome = COALESCE(cantelop_observability.requests.outcome, EXCLUDED.outcome),
  received_at = LEAST(cantelop_observability.requests.received_at, EXCLUDED.received_at),
  completed_at = COALESCE(cantelop_observability.requests.completed_at, EXCLUDED.completed_at),
  incomplete = false
WHERE cantelop_observability.requests.app_id = EXCLUDED.app_id
  AND cantelop_observability.requests.trace_id = EXCLUDED.trace_id`, [
          event.request_id, event.app_id, event.workspace_id ?? null, event.trace_id,
          event.method ?? null, event.path ?? null, event.status_code ?? null,
          event.duration_us ?? null, event.outcome ?? null, event.received_at, event.completed_at,
        ]);
        return;
      case "span.started": {
        const result = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.spans
  (id, trace_id, attempt_id, parent_span_id, name, kind, status, started_at, attributes)
SELECT $1, $2, $3, $4, $5, $6, 'unset', $7, $8::jsonb
FROM cantelop_observability.message_attempts
WHERE id = $3 AND app_id = $9 AND trace_id = $2
  AND ($4::text IS NULL OR EXISTS (
    SELECT 1 FROM cantelop_observability.spans parent
    WHERE parent.id = $4 AND parent.attempt_id = $3 AND parent.trace_id = $2
  ))
ON CONFLICT (id) DO UPDATE SET
  started_at = LEAST(cantelop_observability.spans.started_at, EXCLUDED.started_at),
  attributes = cantelop_observability.spans.attributes || EXCLUDED.attributes
WHERE cantelop_observability.spans.trace_id = EXCLUDED.trace_id
  AND cantelop_observability.spans.attempt_id = EXCLUDED.attempt_id
  AND cantelop_observability.spans.parent_span_id IS NOT DISTINCT FROM EXCLUDED.parent_span_id
  AND cantelop_observability.spans.name = EXCLUDED.name
  AND cantelop_observability.spans.kind = EXCLUDED.kind
RETURNING id`, [
          event.span_id, event.trace_id, event.attempt_id, event.parent_span_id ?? null,
          event.name, event.kind, event.started_at, JSON.stringify(event.attributes), event.app_id,
        ]);
        if (result.rowCount !== 1) throw new ObservationRejectedError("SDK span does not belong to the observed Message attempt");
        return;
      }
      case "span.completed": {
        const result = await client.query(`
UPDATE cantelop_observability.spans
SET status = $4, finished_at = $5, attributes = attributes || $6::jsonb
WHERE id = $1 AND trace_id = $2 AND attempt_id = $3
  AND EXISTS (
    SELECT 1 FROM cantelop_observability.message_attempts attempt
    WHERE attempt.id = $3 AND attempt.app_id = $7 AND attempt.trace_id = $2
  )`, [
          event.span_id, event.trace_id, event.attempt_id, event.status,
          event.finished_at, JSON.stringify(event.attributes), event.app_id,
        ]);
        if (result.rowCount !== 1) throw new ObservationRejectedError("SDK span was not started before it completed");
        return;
      }
      case "log.recorded": {
        if (event.request_id !== undefined) {
          const request = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.requests
  (id, app_id, trace_id, received_at, incomplete)
VALUES ($1, $2, $3, $4, true)
ON CONFLICT (id) DO UPDATE SET received_at = LEAST(cantelop_observability.requests.received_at, EXCLUDED.received_at)
WHERE cantelop_observability.requests.app_id = EXCLUDED.app_id
  AND cantelop_observability.requests.trace_id = EXCLUDED.trace_id
RETURNING id`, [
            event.request_id, event.app_id, event.trace_id, event.occurred_at,
          ]);
          if (request.rowCount !== 1) throw new ObservationRejectedError("Request log does not belong to the observed Request");
          const result = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.logs
  (id, app_id, request_id, trace_id, severity, body, occurred_at, attributes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
ON CONFLICT (id) DO NOTHING
RETURNING id`, [
            event.log_id, event.app_id, event.request_id, event.trace_id,
            event.severity, event.body, event.occurred_at, JSON.stringify(event.attributes),
          ]);
          if (result.rowCount !== 1) throw new ObservationRejectedError("Request log identity conflicts with an existing observation");
          return;
        }
        if (event.attempt_id === undefined) {
          const scope = await this.ensureRuntimeSandbox(client, event);
          const result = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.logs
  (id, app_id, session_id, sandbox_id, severity, body, occurred_at, attributes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
ON CONFLICT (id) DO NOTHING
RETURNING id`, [
            event.log_id, event.app_id, scope.sessionID, event.sandbox_id,
            event.severity, event.body, event.occurred_at, JSON.stringify(event.attributes),
          ]);
          if (result.rowCount !== 1) throw new ObservationRejectedError("Sandbox runtime log identity conflicts with an existing observation");
          return;
        }
        const result = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.logs
  (id, app_id, session_id, sandbox_id, trace_id, attempt_id, span_id, severity, body, occurred_at, attributes)
SELECT $1, $9, message.session_id, attempt.sandbox_id, $2, $3, $4, $5, $6, $7, $8::jsonb
FROM cantelop_observability.message_attempts attempt
JOIN cantelop_observability.messages message ON message.id = attempt.message_id
WHERE attempt.id = $3 AND attempt.app_id = $9 AND attempt.trace_id = $2
  AND ($4::text IS NULL OR EXISTS (
    SELECT 1 FROM cantelop_observability.spans span
    WHERE span.id = $4 AND span.attempt_id = $3 AND span.trace_id = $2
  ))
ON CONFLICT (id) DO NOTHING
RETURNING id`, [
          event.log_id, event.trace_id, event.attempt_id, event.span_id ?? null,
          event.severity, event.body, event.occurred_at, JSON.stringify(event.attributes), event.app_id,
        ]);
        if (result.rowCount !== 1) throw new ObservationRejectedError("SDK log does not belong to the observed Message attempt");
        return;
      }
      case "message.accepted":
        await this.ensureMessage(client, event);
        return;
      case "attempt.started": {
        await this.ensureMessage(client, event);
        const result = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.message_attempts
  (id, message_id, app_id, attempt, trace_id, span_id, queued_at, outcome)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
ON CONFLICT (id) DO UPDATE SET queued_at = LEAST(cantelop_observability.message_attempts.queued_at, EXCLUDED.queued_at)
WHERE cantelop_observability.message_attempts.message_id = EXCLUDED.message_id
  AND cantelop_observability.message_attempts.app_id = EXCLUDED.app_id
  AND cantelop_observability.message_attempts.attempt = EXCLUDED.attempt
  AND cantelop_observability.message_attempts.trace_id = EXCLUDED.trace_id
  AND cantelop_observability.message_attempts.span_id = EXCLUDED.span_id
RETURNING id`, [event.attempt_id, event.message_id, event.app_id, event.attempt, event.trace_id, event.span_id, event.queued_at]);
        if (result.rowCount !== 1) throw new ObservationRejectedError("Message attempt identity conflicts with an existing observation");
        return;
      }
      case "attempt.routed": {
        const sessionID = await this.ensureMessage(client, event);
        const sandbox = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.sandboxes
  (id, session_id, app_id, workspace_id, concrete_session_id, build_id, allocated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
  concrete_session_id = COALESCE(cantelop_observability.sandboxes.concrete_session_id, EXCLUDED.concrete_session_id),
  build_id = COALESCE(cantelop_observability.sandboxes.build_id, EXCLUDED.build_id),
  allocated_at = LEAST(cantelop_observability.sandboxes.allocated_at, EXCLUDED.allocated_at)
WHERE cantelop_observability.sandboxes.session_id = EXCLUDED.session_id
  AND cantelop_observability.sandboxes.app_id = EXCLUDED.app_id
  AND cantelop_observability.sandboxes.workspace_id = EXCLUDED.workspace_id
RETURNING id`, [
          event.sandbox_id, sessionID, event.app_id, event.workspace_id,
          event.concrete_session_id, event.build_id ?? null, event.allocated_at,
        ]);
        if (sandbox.rowCount !== 1) throw new ObservationRejectedError("Sandbox identity conflicts with an existing observation");
        const attempt = await client.query(`
UPDATE cantelop_observability.message_attempts
SET sandbox_id = $2, routed_at = COALESCE(routed_at, $3)
WHERE id = $1 AND app_id = $4 AND (sandbox_id IS NULL OR sandbox_id = $2)`, [
          event.attempt_id, event.sandbox_id, event.occurred_at, event.app_id,
        ]);
        if (attempt.rowCount !== 1) throw new ObservationRejectedError("Message attempt was not started before it was routed");
        return;
      }
      case "attempt.completed": {
        await this.ensureMessage(client, event);
        const result = await client.query(`
UPDATE cantelop_observability.message_attempts
SET generation = $2, quiescent_at = $3, finished_at = $4, outcome = 'completed'
WHERE id = $1 AND app_id = $5 AND outcome IN ('pending', 'completed')`, [
          event.attempt_id, event.generation, event.quiescent_at, event.finished_at, event.app_id,
        ]);
        if (result.rowCount !== 1) throw new ObservationRejectedError("Message attempt was not started before it completed");
        return;
      }
      case "attempt.failed": {
        await this.ensureMessage(client, event);
        const result = await client.query(`
UPDATE cantelop_observability.message_attempts
SET finished_at = $2, outcome = 'failed', error_code = $3, incomplete = $4
WHERE id = $1 AND app_id = $5 AND outcome IN ('pending', 'failed')`, [
          event.attempt_id, event.finished_at, event.error_code, event.incomplete ?? false, event.app_id,
        ]);
        if (result.rowCount !== 1) throw new ObservationRejectedError("Message attempt was not started before it failed");
        return;
      }
    }
  }

  private async ensureRuntimeSandbox(
    client: ObservationTransaction,
    event: Extract<ObservationEvent, { type: "log.recorded" }> & {
      workspace_id: string;
      logical_session_id: string;
      sandbox_id: string;
      concrete_session_id: string;
    },
  ): Promise<{ sessionID: string }> {
    const session = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.sessions
  (app_id, workspace_id, logical_session_id, first_seen_at, last_seen_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (app_id, workspace_id, logical_session_id) DO UPDATE SET
  first_seen_at = LEAST(cantelop_observability.sessions.first_seen_at, EXCLUDED.first_seen_at),
  last_seen_at = GREATEST(cantelop_observability.sessions.last_seen_at, EXCLUDED.last_seen_at)
RETURNING id`, [event.app_id, event.workspace_id, event.logical_session_id, event.occurred_at]);
    const sessionID = session.rows[0]?.id;
    if (!sessionID) throw new Error("could not resolve runtime log Session");
    const sandbox = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.sandboxes
  (id, session_id, app_id, workspace_id, concrete_session_id, allocated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
  concrete_session_id = COALESCE(cantelop_observability.sandboxes.concrete_session_id, EXCLUDED.concrete_session_id),
  allocated_at = LEAST(cantelop_observability.sandboxes.allocated_at, EXCLUDED.allocated_at)
WHERE cantelop_observability.sandboxes.session_id = EXCLUDED.session_id
  AND cantelop_observability.sandboxes.app_id = EXCLUDED.app_id
  AND cantelop_observability.sandboxes.workspace_id = EXCLUDED.workspace_id
RETURNING id`, [
      event.sandbox_id, sessionID, event.app_id, event.workspace_id,
      event.concrete_session_id, event.occurred_at,
    ]);
    if (sandbox.rowCount !== 1) throw new ObservationRejectedError("Sandbox runtime log scope conflicts with an existing observation");
    return { sessionID };
  }

  private async ensureMessage(client: ObservationTransaction, event: MessageEvent): Promise<string> {
    const session = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.sessions
  (app_id, workspace_id, logical_session_id, first_seen_at, last_seen_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (app_id, workspace_id, logical_session_id) DO UPDATE SET
  first_seen_at = LEAST(cantelop_observability.sessions.first_seen_at, EXCLUDED.first_seen_at),
  last_seen_at = GREATEST(cantelop_observability.sessions.last_seen_at, EXCLUDED.last_seen_at)
RETURNING id`, [event.app_id, event.workspace_id, event.logical_session_id, event.occurred_at]);
    const sessionID = session.rows[0]?.id;
    if (!sessionID) throw new Error("could not resolve observed Session");
    const message = await client.query<{ id: string }>(`
INSERT INTO cantelop_observability.messages
  (id, session_id, app_id, workspace_id, request_id, accepted_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
  request_id = COALESCE(cantelop_observability.messages.request_id, EXCLUDED.request_id),
  accepted_at = LEAST(cantelop_observability.messages.accepted_at, EXCLUDED.accepted_at)
WHERE cantelop_observability.messages.session_id = EXCLUDED.session_id
  AND cantelop_observability.messages.app_id = EXCLUDED.app_id
  AND cantelop_observability.messages.workspace_id = EXCLUDED.workspace_id
  AND (cantelop_observability.messages.request_id IS NULL OR EXCLUDED.request_id IS NULL
       OR cantelop_observability.messages.request_id = EXCLUDED.request_id)
RETURNING id`, [event.message_id, sessionID, event.app_id, event.workspace_id, event.request_id ?? null, event.accepted_at]);
    if (message.rowCount !== 1) throw new ObservationRejectedError("Message identity conflicts with an existing observation");
    return sessionID;
  }
}
