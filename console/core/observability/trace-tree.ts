export interface TraceLog {
  id: string;
  span_id?: string;
  severity: string;
  body: string;
  occurred_at: string;
  attributes: Record<string, unknown>;
}

export interface TraceSpan {
  id: string;
  parent_span_id?: string;
  name: string;
  kind: string;
  status: string;
  started_at: string;
  finished_at?: string;
  attributes: Record<string, unknown>;
}

export interface TraceSession {
  id: string;
  workspace_id: string;
  logical_session_id: string;
  first_seen_at: string;
  last_seen_at: string;
}

export interface TraceSandbox {
  id: string;
  concrete_session_id?: string;
  build_id?: string;
  allocated_at: string;
  logs: TraceLog[];
}

export interface TraceAttempt {
  id: string;
  number: number;
  trace_id: string;
  span_id: string;
  outcome: string;
  error_code?: string;
  queued_at: string;
  routed_at?: string;
  finished_at?: string;
  sandbox?: TraceSandbox;
  spans: TraceSpan[];
  logs: TraceLog[];
}

export interface TraceMessage {
  id: string;
  accepted_at: string;
  session: TraceSession;
  attempts: TraceAttempt[];
}

export interface TraceRequest {
  id: string;
  method?: string;
  path?: string;
  status_code?: number;
  duration_us?: number;
  outcome?: string;
  received_at: string;
  completed_at?: string;
  logs: TraceLog[];
  messages: TraceMessage[];
}

export interface AppTraceTree {
  app_id: string;
  requests: TraceRequest[];
  unlinked_messages: TraceMessage[];
}

export interface TraceRow extends Record<string, unknown> {
  root_kind: "request" | "message";
  root_id: string;
  root_at: Date | string;
  request_id: string | null;
  request_method: string | null;
  request_path: string | null;
  request_status_code: number | null;
  request_duration_us: number | null;
  request_outcome: string | null;
  request_received_at: Date | string | null;
  request_completed_at: Date | string | null;
  request_logs: unknown;
  message_id: string | null;
  message_accepted_at: Date | string | null;
  session_id: string | null;
  workspace_id: string | null;
  logical_session_id: string | null;
  session_first_seen_at: Date | string | null;
  session_last_seen_at: Date | string | null;
  sandbox_id: string | null;
  concrete_session_id: string | null;
  build_id: string | null;
  sandbox_allocated_at: Date | string | null;
  sandbox_logs: unknown;
  attempt_id: string | null;
  attempt_number: number | null;
  trace_id: string | null;
  attempt_span_id: string | null;
  attempt_outcome: string | null;
  attempt_error_code: string | null;
  attempt_queued_at: Date | string | null;
  attempt_routed_at: Date | string | null;
  attempt_finished_at: Date | string | null;
  spans: unknown;
  logs: unknown;
}

export function buildAppTraceTree(appID: string, rows: TraceRow[]): AppTraceTree {
  const requests = new Map<string, TraceRequest>();
  const unlinkedMessages = new Map<string, TraceMessage>();
  for (const row of rows) {
    let request: TraceRequest | undefined;
    if (row.root_kind === "request") {
      if (row.request_id === null || row.request_received_at === null) {
        throw new TypeError("request trace row is incomplete");
      }
      request = requests.get(row.request_id);
      if (!request) {
        request = {
          id: row.request_id,
          ...(row.request_method === null ? {} : { method: row.request_method }),
          ...(row.request_path === null ? {} : { path: row.request_path }),
          ...(row.request_status_code === null ? {} : { status_code: row.request_status_code }),
          ...(row.request_duration_us === null ? {} : { duration_us: row.request_duration_us }),
          ...(row.request_outcome === null ? {} : { outcome: row.request_outcome }),
          received_at: instant(row.request_received_at),
          ...(row.request_completed_at === null ? {} : { completed_at: instant(row.request_completed_at) }),
          logs: observedLogs(row.request_logs),
          messages: [],
        };
        requests.set(request.id, request);
      }
    }
    if (row.message_id === null) continue;
    if (row.message_accepted_at === null || row.session_id === null || row.workspace_id === null ||
        row.logical_session_id === null || row.session_first_seen_at === null || row.session_last_seen_at === null) {
      throw new TypeError("message trace row is incomplete");
    }
    const messageList = request?.messages;
    let message = messageList?.find(({ id }) => id === row.message_id) ?? unlinkedMessages.get(row.message_id);
    if (!message) {
      message = {
        id: row.message_id,
        accepted_at: instant(row.message_accepted_at),
        session: {
          id: row.session_id,
          workspace_id: row.workspace_id,
          logical_session_id: row.logical_session_id,
          first_seen_at: instant(row.session_first_seen_at),
          last_seen_at: instant(row.session_last_seen_at),
        },
        attempts: [],
      };
      if (messageList) messageList.push(message);
      else unlinkedMessages.set(message.id, message);
    }
    if (row.attempt_id !== null && row.attempt_number !== null && row.trace_id !== null &&
        row.attempt_span_id !== null && row.attempt_outcome !== null && row.attempt_queued_at !== null) {
      message.attempts.push({
        id: row.attempt_id,
        number: row.attempt_number,
        trace_id: row.trace_id,
        span_id: row.attempt_span_id,
        outcome: row.attempt_outcome,
        ...(row.attempt_error_code === null ? {} : { error_code: row.attempt_error_code }),
        queued_at: instant(row.attempt_queued_at),
        ...(row.attempt_routed_at === null ? {} : { routed_at: instant(row.attempt_routed_at) }),
        ...(row.attempt_finished_at === null ? {} : { finished_at: instant(row.attempt_finished_at) }),
        ...(row.sandbox_id === null || row.sandbox_allocated_at === null ? {} : { sandbox: {
          id: row.sandbox_id,
          ...(row.concrete_session_id === null ? {} : { concrete_session_id: row.concrete_session_id }),
          ...(row.build_id === null ? {} : { build_id: row.build_id }),
          allocated_at: instant(row.sandbox_allocated_at),
          logs: observedLogs(row.sandbox_logs),
        } }),
        spans: observedArray<TraceSpan>(row.spans),
        logs: observedLogs(row.logs),
      });
    }
  }
  return { app_id: appID, requests: [...requests.values()], unlinked_messages: [...unlinkedMessages.values()] };
}

function instant(value: Date | string): string {
  const date = value instanceof Date ? value : new Date(value);
  if (!Number.isFinite(date.valueOf())) throw new TypeError("trace row contains an invalid timestamp");
  return date.toISOString();
}

function observedArray<Value>(value: unknown): Value[] {
  if (Array.isArray(value)) return value as Value[];
  if (typeof value === "string") {
    const parsed = JSON.parse(value) as unknown;
    if (Array.isArray(parsed)) return parsed as Value[];
  }
  return [];
}

function observedLogs(value: unknown): TraceLog[] {
  return observedArray<TraceLog>(value).map((log) => ({
    ...log,
    severity: isSDKLifecycleRecord(log.body) ? "info" : log.severity,
  }));
}

function isSDKLifecycleRecord(body: string): boolean {
  try {
    const parsed = JSON.parse(body) as unknown;
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return false;
    const record = parsed as Record<string, unknown>;
    return record.component === "cantelop.sdk" &&
      (record.event === "message_lifecycle" || record.event === "session_runtime_startup_stage");
  } catch {
    return false;
  }
}
