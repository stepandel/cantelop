import { sql } from "drizzle-orm";
import {
  bigint,
  boolean,
  check,
  index,
  integer,
  jsonb,
  pgSchema,
  primaryKey,
  text,
  timestamp,
  unique,
  uuid,
  type AnyPgColumn,
} from "drizzle-orm/pg-core";

const observability = pgSchema("cantelop_observability");
const timestampTZ = (name: string) => timestamp(name, { withTimezone: true, mode: "date" });

export const requests = observability.table("requests", {
  id: text().primaryKey(),
  appId: text("app_id").notNull(),
  workspaceId: text("workspace_id"),
  traceId: text("trace_id").notNull(),
  method: text(),
  path: text(),
  statusCode: integer("status_code"),
  durationUs: bigint("duration_us", { mode: "number" }),
  outcome: text(),
  receivedAt: timestampTZ("received_at").notNull(),
  completedAt: timestampTZ("completed_at"),
  incomplete: boolean().notNull().default(false),
}, (table) => [
  check("requests_id_check", sql`${table.id} ~ '^req_[0-9a-f]{32}$'`),
  check("requests_app_id_check", sql`${table.appId} ~ '^app_[0-9a-f]{32}$'`),
  check("requests_workspace_id_check", sql`${table.workspaceId} IS NULL OR ${table.workspaceId} ~ '^wsp_[0-9a-f]{32}$'`),
  check("requests_trace_id_check", sql`${table.traceId} ~ '^[0-9a-f]{32}$' AND ${table.traceId} <> repeat('0', 32)`),
  check("requests_status_code_check", sql`${table.statusCode} IS NULL OR ${table.statusCode} BETWEEN 100 AND 599`),
  check("requests_duration_check", sql`${table.durationUs} IS NULL OR ${table.durationUs} >= 0`),
  check("requests_completed_check", sql`${table.completedAt} IS NULL OR ${table.completedAt} >= ${table.receivedAt}`),
  index("requests_app_received").on(table.appId, table.receivedAt, table.id),
  // Retention sweeps scan by age across every App.
  index("requests_received").on(table.receivedAt),
]);

export const sessions = observability.table("sessions", {
  id: uuid().primaryKey().defaultRandom(),
  appId: text("app_id").notNull(),
  workspaceId: text("workspace_id").notNull(),
  logicalSessionId: text("logical_session_id").notNull(),
  firstSeenAt: timestampTZ("first_seen_at").notNull(),
  lastSeenAt: timestampTZ("last_seen_at").notNull(),
}, (table) => [
  check("sessions_app_id_check", sql`${table.appId} ~ '^app_[0-9a-f]{32}$'`),
  check("sessions_workspace_id_check", sql`${table.workspaceId} ~ '^wsp_[0-9a-f]{32}$'`),
  check("sessions_logical_id_check", sql`${table.logicalSessionId} ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'`),
  check("sessions_seen_check", sql`${table.lastSeenAt} >= ${table.firstSeenAt}`),
  unique("sessions_logical_unique").on(table.appId, table.workspaceId, table.logicalSessionId),
  index("sessions_app_seen").on(table.appId, table.lastSeenAt, table.id),
  index("sessions_last_seen").on(table.lastSeenAt),
]);

export const sandboxes = observability.table("sandboxes", {
  id: text().primaryKey(),
  sessionId: uuid("session_id").notNull().references(() => sessions.id, { onDelete: "cascade" }),
  appId: text("app_id").notNull(),
  workspaceId: text("workspace_id").notNull(),
  concreteSessionId: text("concrete_session_id"),
  buildId: text("build_id"),
  allocatedAt: timestampTZ("allocated_at").notNull(),
  terminatedAt: timestampTZ("terminated_at"),
  outcome: text(),
}, (table) => [
  check("sandboxes_id_check", sql`${table.id} ~ '^sbx-[0-9a-f]{32}$'`),
  check("sandboxes_app_id_check", sql`${table.appId} ~ '^app_[0-9a-f]{32}$'`),
  check("sandboxes_workspace_id_check", sql`${table.workspaceId} ~ '^wsp_[0-9a-f]{32}$'`),
  check("sandboxes_concrete_session_id_check", sql`${table.concreteSessionId} IS NULL OR ${table.concreteSessionId} ~ '^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$'`),
  check("sandboxes_terminated_check", sql`${table.terminatedAt} IS NULL OR ${table.terminatedAt} >= ${table.allocatedAt}`),
  index("sandboxes_session_allocated").on(table.sessionId, table.allocatedAt, table.id),
  index("sandboxes_app_allocated").on(table.appId, table.allocatedAt, table.id),
  index("sandboxes_allocated").on(table.allocatedAt),
]);

export const messages = observability.table("messages", {
  id: text().primaryKey(),
  sessionId: uuid("session_id").notNull().references(() => sessions.id, { onDelete: "cascade" }),
  appId: text("app_id").notNull(),
  workspaceId: text("workspace_id").notNull(),
  requestId: text("request_id"),
  acceptedAt: timestampTZ("accepted_at").notNull(),
}, (table) => [
  check("messages_id_check", sql`${table.id} ~ '^msg_[0-9a-f]{32}$'`),
  check("messages_app_id_check", sql`${table.appId} ~ '^app_[0-9a-f]{32}$'`),
  check("messages_workspace_id_check", sql`${table.workspaceId} ~ '^wsp_[0-9a-f]{32}$'`),
  check("messages_request_id_check", sql`${table.requestId} IS NULL OR ${table.requestId} ~ '^req_[0-9a-f]{32}$'`),
  index("messages_session_accepted").on(table.sessionId, table.acceptedAt, table.id),
  index("messages_request").on(table.requestId, table.id),
  index("messages_accepted").on(table.acceptedAt),
]);

export const messageAttempts = observability.table("message_attempts", {
  id: text().primaryKey(),
  messageId: text("message_id").notNull().references(() => messages.id, { onDelete: "cascade" }),
  sandboxId: text("sandbox_id").references(() => sandboxes.id, { onDelete: "set null" }),
  appId: text("app_id").notNull(),
  attempt: integer().notNull(),
  traceId: text("trace_id").notNull(),
  spanId: text("span_id").notNull(),
  queuedAt: timestampTZ("queued_at").notNull(),
  routedAt: timestampTZ("routed_at"),
  deliveredAt: timestampTZ("delivered_at"),
  handlerStartedAt: timestampTZ("handler_started_at"),
  handlerFinishedAt: timestampTZ("handler_finished_at"),
  quiescentAt: timestampTZ("quiescent_at"),
  finishedAt: timestampTZ("finished_at"),
  generation: bigint({ mode: "number" }),
  outcome: text().$type<"pending" | "completed" | "failed">().notNull(),
  errorCode: text("error_code"),
  incomplete: boolean().notNull().default(false),
}, (table) => [
  check("message_attempts_id_check", sql`${table.id} ~ '^att_[0-9a-f]{32}$'`),
  check("message_attempts_app_id_check", sql`${table.appId} ~ '^app_[0-9a-f]{32}$'`),
  check("message_attempts_attempt_check", sql`${table.attempt} BETWEEN 1 AND 100`),
  check("message_attempts_trace_id_check", sql`${table.traceId} ~ '^[0-9a-f]{32}$' AND ${table.traceId} <> repeat('0', 32)`),
  check("message_attempts_span_id_check", sql`${table.spanId} ~ '^[0-9a-f]{16}$' AND ${table.spanId} <> repeat('0', 16)`),
  check("message_attempts_generation_check", sql`${table.generation} IS NULL OR ${table.generation} BETWEEN 1 AND 9007199254740991`),
  check("message_attempts_outcome_check", sql`${table.outcome} IN ('pending', 'completed', 'failed')`),
  check("message_attempts_error_code_check", sql`${table.errorCode} IS NULL OR ${table.errorCode} ~ '^[a-z][a-z0-9_]{0,63}$'`),
  unique("message_attempts_message_attempt_unique").on(table.messageId, table.attempt),
  unique("message_attempts_span_unique").on(table.spanId),
  index("message_attempts_sandbox_queued").on(table.sandboxId, table.queuedAt, table.id),
  index("message_attempts_app_queued").on(table.appId, table.queuedAt, table.id),
]);

export const spans = observability.table("spans", {
  id: text().primaryKey(),
  attemptId: text("attempt_id").notNull().references(() => messageAttempts.id, { onDelete: "cascade" }),
  traceId: text("trace_id").notNull(),
  parentSpanId: text("parent_span_id").references((): AnyPgColumn => spans.id, { onDelete: "cascade" }),
  name: text().notNull(),
  kind: text().$type<"internal" | "client" | "server">().notNull(),
  startedAt: timestampTZ("started_at").notNull(),
  finishedAt: timestampTZ("finished_at"),
  status: text().$type<"unset" | "ok" | "error">().notNull(),
  attributes: jsonb().$type<Record<string, unknown>>().notNull().default({}),
}, (table) => [
  check("spans_id_check", sql`${table.id} ~ '^[0-9a-f]{16}$' AND ${table.id} <> repeat('0', 16)`),
  check("spans_trace_id_check", sql`${table.traceId} ~ '^[0-9a-f]{32}$' AND ${table.traceId} <> repeat('0', 32)`),
  check("spans_name_check", sql`length(${table.name}) BETWEEN 1 AND 256`),
  check("spans_kind_check", sql`${table.kind} IN ('internal', 'client', 'server')`),
  check("spans_status_check", sql`${table.status} IN ('unset', 'ok', 'error')`),
  check("spans_finished_check", sql`${table.finishedAt} IS NULL OR ${table.finishedAt} >= ${table.startedAt}`),
  check("spans_attributes_check", sql`jsonb_typeof(${table.attributes}) = 'object'`),
  index("spans_attempt_started").on(table.attemptId, table.startedAt, table.id),
  index("spans_trace_started").on(table.traceId, table.startedAt, table.id),
]);

export const logs = observability.table("logs", {
  id: text().primaryKey(),
  appId: text("app_id").notNull(),
  requestId: text("request_id").references(() => requests.id, { onDelete: "cascade" }),
  sessionId: uuid("session_id").references(() => sessions.id, { onDelete: "cascade" }),
  sandboxId: text("sandbox_id").references(() => sandboxes.id, { onDelete: "cascade" }),
  attemptId: text("attempt_id").references(() => messageAttempts.id, { onDelete: "cascade" }),
  spanId: text("span_id").references(() => spans.id, { onDelete: "cascade" }),
  traceId: text("trace_id"),
  occurredAt: timestampTZ("occurred_at").notNull(),
  severity: text().notNull(),
  body: text().notNull(),
  attributes: jsonb().$type<Record<string, unknown>>().notNull().default({}),
}, (table) => [
  check("logs_id_check", sql`${table.id} ~ '^log_[0-9a-f]{32}$'`),
  check("logs_app_id_check", sql`${table.appId} ~ '^app_[0-9a-f]{32}$'`),
  check("logs_trace_id_check", sql`${table.traceId} IS NULL OR (${table.traceId} ~ '^[0-9a-f]{32}$' AND ${table.traceId} <> repeat('0', 32))`),
  check("logs_scope_check", sql`
    (${table.requestId} IS NOT NULL AND ${table.traceId} IS NOT NULL AND ${table.attemptId} IS NULL AND ${table.spanId} IS NULL AND ${table.sessionId} IS NULL AND ${table.sandboxId} IS NULL)
    OR (${table.requestId} IS NULL AND ${table.attemptId} IS NOT NULL AND ${table.traceId} IS NOT NULL)
    OR (${table.requestId} IS NULL AND ${table.attemptId} IS NULL AND ${table.traceId} IS NULL AND ${table.spanId} IS NULL AND ${table.sessionId} IS NOT NULL AND ${table.sandboxId} IS NOT NULL)`),
  check("logs_severity_check", sql`length(${table.severity}) BETWEEN 1 AND 32`),
  check("logs_body_check", sql`length(${table.body}) BETWEEN 1 AND 16384`),
  check("logs_attributes_check", sql`jsonb_typeof(${table.attributes}) = 'object'`),
  index("logs_request_occurred").on(table.requestId, table.occurredAt, table.id),
  index("logs_attempt_occurred").on(table.attemptId, table.occurredAt, table.id),
  index("logs_span_occurred").on(table.spanId, table.occurredAt, table.id),
  index("logs_sandbox_occurred").on(table.sandboxId, table.occurredAt, table.id),
  // Session deletes probe (and cascade through) logs.session_id.
  index("logs_session").on(table.sessionId),
  index("logs_occurred").on(table.occurredAt),
]);

export const ingestionReceipts = observability.table("ingestion_receipts", {
  source: text().notNull(),
  eventId: text("event_id").notNull(),
  schemaVersion: integer("schema_version").notNull(),
  receivedAt: timestampTZ("received_at").notNull(),
}, (table) => [
  primaryKey({ columns: [table.source, table.eventId] }),
  check("ingestion_receipts_source_check", sql`${table.source} IN ('edge', 'gateway', 'fire_fuse', 'sdk')`),
  check("ingestion_receipts_event_id_check", sql`length(${table.eventId}) BETWEEN 1 AND 128`),
  check("ingestion_receipts_schema_version_check", sql`${table.schemaVersion} BETWEEN 1 AND 1000`),
  index("ingestion_receipts_received").on(table.receivedAt, table.source, table.eventId),
]);

export const retentionState = observability.table("retention_state", {
  id: integer().primaryKey(),
  lastRunAt: timestampTZ("last_run_at").notNull(),
}, (table) => [
  check("retention_state_singleton_check", sql`${table.id} = 1`),
]);

export const observabilitySchema = {
  requests,
  sessions,
  sandboxes,
  messages,
  messageAttempts,
  spans,
  logs,
  ingestionReceipts,
  retentionState,
};
