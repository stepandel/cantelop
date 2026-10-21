export const MAX_OBSERVATION_BATCH = 256;

export type ObservationSource = "edge" | "gateway" | "fire_fuse" | "sdk";

interface EventBase {
  event_id: string;
  schema_version: 1;
  source: ObservationSource;
  occurred_at: string;
  app_id: string;
}

interface MessageScope {
  workspace_id: string;
  logical_session_id: string;
  message_id: string;
  request_id?: string;
  accepted_at: string;
}

export type ObservationEvent =
  | (EventBase & {
      type: "request.completed";
      request_id: string;
      workspace_id?: string;
      trace_id: string;
      received_at: string;
      completed_at: string;
      method?: string;
      path?: string;
      status_code?: number;
      duration_us?: number;
      outcome?: string;
    })
  | (EventBase & MessageScope & { type: "message.accepted" })
  | (EventBase & MessageScope & {
      type: "attempt.started";
      attempt_id: string;
      attempt: number;
      trace_id: string;
      span_id: string;
      queued_at: string;
    })
  | (EventBase & MessageScope & {
      type: "attempt.routed";
      attempt_id: string;
      sandbox_id: string;
      concrete_session_id: string;
      build_id?: string;
      allocated_at: string;
    })
  | (EventBase & MessageScope & {
      type: "attempt.completed";
      attempt_id: string;
      generation: number;
      quiescent_at: string;
      finished_at: string;
    })
  | (EventBase & MessageScope & {
      type: "attempt.failed";
      attempt_id: string;
      error_code: string;
      finished_at: string;
      incomplete?: boolean;
    })
  | (EventBase & {
      type: "span.started";
      attempt_id: string;
      trace_id: string;
      span_id: string;
      parent_span_id?: string;
      name: string;
      kind: "internal" | "client" | "server";
      started_at: string;
      attributes: Record<string, unknown>;
    })
  | (EventBase & {
      type: "span.completed";
      attempt_id: string;
      trace_id: string;
      span_id: string;
      status: "unset" | "ok" | "error";
      finished_at: string;
      attributes: Record<string, unknown>;
    })
  | (EventBase & {
      type: "log.recorded";
      log_id: string;
      severity: "debug" | "info" | "warn" | "error";
      body: string;
      attributes: Record<string, unknown>;
    } & ({
      request_id: string;
      trace_id: string;
      attempt_id?: undefined;
      span_id?: undefined;
      workspace_id?: undefined;
      logical_session_id?: undefined;
      sandbox_id?: undefined;
      concrete_session_id?: undefined;
    } | {
      request_id?: undefined;
      attempt_id: string;
      trace_id: string;
      span_id?: string;
      workspace_id?: string;
      logical_session_id?: string;
      sandbox_id?: string;
      concrete_session_id?: string;
    } | {
      request_id?: undefined;
      workspace_id: string;
      logical_session_id: string;
      sandbox_id: string;
      concrete_session_id: string;
      attempt_id?: undefined;
      trace_id?: undefined;
      span_id?: undefined;
    }));

export interface ObservationBatch {
  events: ObservationEvent[];
}

const patterns = {
  event: /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/,
  app: /^app_[0-9a-f]{32}$/,
  workspace: /^wsp_[0-9a-f]{32}$/,
  request: /^req_[0-9a-f]{32}$/,
  message: /^msg_[0-9a-f]{32}$/,
  attempt: /^att_[0-9a-f]{32}$/,
  sandbox: /^sbx-[0-9a-f]{32}$/,
  concreteSession: /^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$/,
  logicalSession: /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/,
  trace: /^(?!0{32}$)[0-9a-f]{32}$/,
  span: /^(?!0{16}$)[0-9a-f]{16}$/,
  log: /^log_[0-9a-f]{32}$/,
  error: /^[a-z][a-z0-9_]{0,63}$/,
};

export function parseObservationBatch(value: unknown): ObservationBatch {
  const record = object(value, "batch");
  if (!Array.isArray(record.events) || record.events.length < 1 || record.events.length > MAX_OBSERVATION_BATCH) {
    throw new TypeError("observation batch must contain 1-256 events");
  }
  return { events: record.events.map(parseObservationEvent) };
}

export function parseObservationEvent(value: unknown): ObservationEvent {
  const event = object(value, "event");
  const base = {
    event_id: matched(event.event_id, patterns.event, "event_id"),
    schema_version: exactNumber(event.schema_version, 1, "schema_version") as 1,
    source: oneOf(event.source, ["edge", "gateway", "fire_fuse", "sdk"] as const, "source"),
    occurred_at: timestamp(event.occurred_at, "occurred_at"),
    app_id: matched(event.app_id, patterns.app, "app_id"),
  };
  const type = oneOf(event.type, [
    "request.completed", "message.accepted", "attempt.started", "attempt.routed",
    "attempt.completed", "attempt.failed", "span.started", "span.completed", "log.recorded",
  ] as const, "type");
  if (type === "request.completed") {
    return {
      ...base, type,
      request_id: matched(event.request_id, patterns.request, "request_id"),
      ...(event.workspace_id === undefined ? {} : { workspace_id: matched(event.workspace_id, patterns.workspace, "workspace_id") }),
      trace_id: matched(event.trace_id, patterns.trace, "trace_id"),
      received_at: timestamp(event.received_at, "received_at"),
      completed_at: timestamp(event.completed_at, "completed_at"),
      ...optionalStringFields(event, ["method", "path", "outcome"]),
      ...(event.status_code === undefined ? {} : { status_code: integer(event.status_code, 100, 599, "status_code") }),
      ...(event.duration_us === undefined ? {} : { duration_us: integer(event.duration_us, 0, Number.MAX_SAFE_INTEGER, "duration_us") }),
    };
  }
  if (type === "span.started") {
    return {
      ...base, type,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      trace_id: matched(event.trace_id, patterns.trace, "trace_id"),
      span_id: matched(event.span_id, patterns.span, "span_id"),
      ...(event.parent_span_id === undefined ? {} : { parent_span_id: matched(event.parent_span_id, patterns.span, "parent_span_id") }),
      name: string(event.name, "name", 256),
      kind: oneOf(event.kind, ["internal", "client", "server"] as const, "kind"),
      started_at: timestamp(event.started_at, "started_at"),
      attributes: parseAttributes(event.attributes),
    };
  }
  if (type === "span.completed") {
    return {
      ...base, type,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      trace_id: matched(event.trace_id, patterns.trace, "trace_id"),
      span_id: matched(event.span_id, patterns.span, "span_id"),
      status: oneOf(event.status, ["unset", "ok", "error"] as const, "status"),
      finished_at: timestamp(event.finished_at, "finished_at"),
      attributes: parseAttributes(event.attributes),
    };
  }
  if (type === "log.recorded") {
    const log = {
      ...base, type,
      log_id: matched(event.log_id, patterns.log, "log_id"),
      severity: oneOf(event.severity, ["debug", "info", "warn", "error"] as const, "severity"),
      body: string(event.body, "body", 16_384),
      attributes: parseAttributes(event.attributes),
    };
    if (event.request_id !== undefined) {
      if (event.attempt_id !== undefined || event.span_id !== undefined || event.workspace_id !== undefined ||
          event.logical_session_id !== undefined || event.sandbox_id !== undefined || event.concrete_session_id !== undefined) {
        throw new TypeError("request log cannot carry Message or Sandbox scope");
      }
      return {
        ...log,
        request_id: matched(event.request_id, patterns.request, "request_id"),
        trace_id: matched(event.trace_id, patterns.trace, "trace_id"),
      };
    }
    if (event.attempt_id === undefined) {
      if (event.trace_id !== undefined || event.span_id !== undefined) {
        throw new TypeError("sandbox runtime log cannot carry attempt trace identity");
      }
      return {
        ...log,
        workspace_id: matched(event.workspace_id, patterns.workspace, "workspace_id"),
        logical_session_id: matched(event.logical_session_id, patterns.logicalSession, "logical_session_id"),
        sandbox_id: matched(event.sandbox_id, patterns.sandbox, "sandbox_id"),
        concrete_session_id: matched(event.concrete_session_id, patterns.concreteSession, "concrete_session_id"),
      };
    }
    return {
      ...log,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      trace_id: matched(event.trace_id, patterns.trace, "trace_id"),
      ...(event.span_id === undefined ? {} : { span_id: matched(event.span_id, patterns.span, "span_id") }),
      ...(event.workspace_id === undefined ? {} : { workspace_id: matched(event.workspace_id, patterns.workspace, "workspace_id") }),
      ...(event.logical_session_id === undefined ? {} : { logical_session_id: matched(event.logical_session_id, patterns.logicalSession, "logical_session_id") }),
      ...(event.sandbox_id === undefined ? {} : { sandbox_id: matched(event.sandbox_id, patterns.sandbox, "sandbox_id") }),
      ...(event.concrete_session_id === undefined ? {} : { concrete_session_id: matched(event.concrete_session_id, patterns.concreteSession, "concrete_session_id") }),
    };
  }
  const scope = parseMessageScope(event);
  switch (type) {
    case "message.accepted": return { ...base, ...scope, type };
    case "attempt.started": return {
      ...base, ...scope, type,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      attempt: integer(event.attempt, 1, 100, "attempt"),
      trace_id: matched(event.trace_id, patterns.trace, "trace_id"),
      span_id: matched(event.span_id, patterns.span, "span_id"),
      queued_at: timestamp(event.queued_at, "queued_at"),
    };
    case "attempt.routed": return {
      ...base, ...scope, type,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      sandbox_id: matched(event.sandbox_id, patterns.sandbox, "sandbox_id"),
      concrete_session_id: matched(event.concrete_session_id, patterns.concreteSession, "concrete_session_id"),
      ...(event.build_id === undefined ? {} : { build_id: string(event.build_id, "build_id", 256) }),
      allocated_at: timestamp(event.allocated_at, "allocated_at"),
    };
    case "attempt.completed": return {
      ...base, ...scope, type,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      generation: integer(event.generation, 1, Number.MAX_SAFE_INTEGER, "generation"),
      quiescent_at: timestamp(event.quiescent_at, "quiescent_at"),
      finished_at: timestamp(event.finished_at, "finished_at"),
    };
    case "attempt.failed": return {
      ...base, ...scope, type,
      attempt_id: matched(event.attempt_id, patterns.attempt, "attempt_id"),
      error_code: matched(event.error_code, patterns.error, "error_code"),
      finished_at: timestamp(event.finished_at, "finished_at"),
      ...(event.incomplete === undefined ? {} : { incomplete: boolean(event.incomplete, "incomplete") }),
    };
  }
}

function parseAttributes(value: unknown): Record<string, unknown> {
  const result = object(value, "attributes");
  if (Buffer.byteLength(JSON.stringify(result)) > 16_384) throw new TypeError("attributes are too large");
  return result;
}

function parseMessageScope(event: Record<string, unknown>): MessageScope {
  return {
    workspace_id: matched(event.workspace_id, patterns.workspace, "workspace_id"),
    logical_session_id: matched(event.logical_session_id, patterns.logicalSession, "logical_session_id"),
    message_id: matched(event.message_id, patterns.message, "message_id"),
    ...(event.request_id === undefined ? {} : { request_id: matched(event.request_id, patterns.request, "request_id") }),
    accepted_at: timestamp(event.accepted_at, "accepted_at"),
  };
}

function object(value: unknown, name: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new TypeError(`${name} must be an object`);
  return value as Record<string, unknown>;
}

function matched(value: unknown, pattern: RegExp, name: string): string {
  if (typeof value !== "string" || !pattern.test(value)) throw new TypeError(`${name} is invalid`);
  return value;
}

function string(value: unknown, name: string, max: number): string {
  if (typeof value !== "string" || value.length < 1 || value.length > max || /[\0\r]/.test(value)) throw new TypeError(`${name} is invalid`);
  return value;
}

function timestamp(value: unknown, name: string): string {
  const result = string(value, name, 64);
  if (!/^\d{4}-\d{2}-\d{2}T/.test(result) || !Number.isFinite(Date.parse(result))) throw new TypeError(`${name} is invalid`);
  return result;
}

function integer(value: unknown, min: number, max: number, name: string): number {
  if (!Number.isSafeInteger(value) || Number(value) < min || Number(value) > max) throw new TypeError(`${name} is invalid`);
  return Number(value);
}

function exactNumber(value: unknown, expected: number, name: string): number {
  if (value !== expected) throw new TypeError(`${name} is unsupported`);
  return expected;
}

function boolean(value: unknown, name: string): boolean {
  if (typeof value !== "boolean") throw new TypeError(`${name} is invalid`);
  return value;
}

function oneOf<const Values extends readonly string[]>(value: unknown, values: Values, name: string): Values[number] {
  if (typeof value !== "string" || !values.includes(value)) throw new TypeError(`${name} is invalid`);
  return value as Values[number];
}

function optionalStringFields(record: Record<string, unknown>, names: string[]): Record<string, string> {
  return Object.fromEntries(names.flatMap((name) => record[name] === undefined ? [] : [[name, string(record[name], name, name === "path" ? 2048 : 256)]]));
}
