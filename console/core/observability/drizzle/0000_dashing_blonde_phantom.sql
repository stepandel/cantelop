CREATE SCHEMA IF NOT EXISTS "cantelop_observability";
--> statement-breakpoint
REVOKE ALL ON SCHEMA "cantelop_observability" FROM PUBLIC;
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."ingestion_receipts" (
	"source" text NOT NULL,
	"event_id" text NOT NULL,
	"schema_version" integer NOT NULL,
	"received_at" timestamp with time zone NOT NULL,
	CONSTRAINT "ingestion_receipts_source_event_id_pk" PRIMARY KEY("source","event_id"),
	CONSTRAINT "ingestion_receipts_source_check" CHECK ("cantelop_observability"."ingestion_receipts"."source" IN ('edge', 'gateway', 'fire_fuse', 'sdk')),
	CONSTRAINT "ingestion_receipts_event_id_check" CHECK (length("cantelop_observability"."ingestion_receipts"."event_id") BETWEEN 1 AND 128),
	CONSTRAINT "ingestion_receipts_schema_version_check" CHECK ("cantelop_observability"."ingestion_receipts"."schema_version" BETWEEN 1 AND 1000)
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."logs" (
	"id" text PRIMARY KEY NOT NULL,
	"app_id" text NOT NULL,
	"session_id" uuid,
	"sandbox_id" text,
	"attempt_id" text,
	"span_id" text,
	"trace_id" text,
	"occurred_at" timestamp with time zone NOT NULL,
	"severity" text NOT NULL,
	"body" text NOT NULL,
	"attributes" jsonb DEFAULT '{}'::jsonb NOT NULL,
	CONSTRAINT "logs_id_check" CHECK ("cantelop_observability"."logs"."id" ~ '^log_[0-9a-f]{32}$'),
	CONSTRAINT "logs_app_id_check" CHECK ("cantelop_observability"."logs"."app_id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "logs_trace_id_check" CHECK ("cantelop_observability"."logs"."trace_id" IS NULL OR ("cantelop_observability"."logs"."trace_id" ~ '^[0-9a-f]{32}$' AND "cantelop_observability"."logs"."trace_id" <> repeat('0', 32))),
	CONSTRAINT "logs_scope_check" CHECK (("cantelop_observability"."logs"."attempt_id" IS NOT NULL AND "cantelop_observability"."logs"."trace_id" IS NOT NULL) OR ("cantelop_observability"."logs"."attempt_id" IS NULL AND "cantelop_observability"."logs"."trace_id" IS NULL AND "cantelop_observability"."logs"."span_id" IS NULL AND "cantelop_observability"."logs"."session_id" IS NOT NULL AND "cantelop_observability"."logs"."sandbox_id" IS NOT NULL)),
	CONSTRAINT "logs_severity_check" CHECK (length("cantelop_observability"."logs"."severity") BETWEEN 1 AND 32),
	CONSTRAINT "logs_body_check" CHECK (length("cantelop_observability"."logs"."body") BETWEEN 1 AND 16384),
	CONSTRAINT "logs_attributes_check" CHECK (jsonb_typeof("cantelop_observability"."logs"."attributes") = 'object')
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."message_attempts" (
	"id" text PRIMARY KEY NOT NULL,
	"message_id" text NOT NULL,
	"sandbox_id" text,
	"app_id" text NOT NULL,
	"attempt" integer NOT NULL,
	"trace_id" text NOT NULL,
	"span_id" text NOT NULL,
	"queued_at" timestamp with time zone NOT NULL,
	"routed_at" timestamp with time zone,
	"delivered_at" timestamp with time zone,
	"handler_started_at" timestamp with time zone,
	"handler_finished_at" timestamp with time zone,
	"quiescent_at" timestamp with time zone,
	"finished_at" timestamp with time zone,
	"generation" bigint,
	"outcome" text NOT NULL,
	"error_code" text,
	"incomplete" boolean DEFAULT false NOT NULL,
	CONSTRAINT "message_attempts_message_attempt_unique" UNIQUE("message_id","attempt"),
	CONSTRAINT "message_attempts_span_unique" UNIQUE("span_id"),
	CONSTRAINT "message_attempts_id_check" CHECK ("cantelop_observability"."message_attempts"."id" ~ '^att_[0-9a-f]{32}$'),
	CONSTRAINT "message_attempts_app_id_check" CHECK ("cantelop_observability"."message_attempts"."app_id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "message_attempts_attempt_check" CHECK ("cantelop_observability"."message_attempts"."attempt" BETWEEN 1 AND 100),
	CONSTRAINT "message_attempts_trace_id_check" CHECK ("cantelop_observability"."message_attempts"."trace_id" ~ '^[0-9a-f]{32}$' AND "cantelop_observability"."message_attempts"."trace_id" <> repeat('0', 32)),
	CONSTRAINT "message_attempts_span_id_check" CHECK ("cantelop_observability"."message_attempts"."span_id" ~ '^[0-9a-f]{16}$' AND "cantelop_observability"."message_attempts"."span_id" <> repeat('0', 16)),
	CONSTRAINT "message_attempts_generation_check" CHECK ("cantelop_observability"."message_attempts"."generation" IS NULL OR "cantelop_observability"."message_attempts"."generation" BETWEEN 1 AND 9007199254740991),
	CONSTRAINT "message_attempts_outcome_check" CHECK ("cantelop_observability"."message_attempts"."outcome" IN ('pending', 'completed', 'failed')),
	CONSTRAINT "message_attempts_error_code_check" CHECK ("cantelop_observability"."message_attempts"."error_code" IS NULL OR "cantelop_observability"."message_attempts"."error_code" ~ '^[a-z][a-z0-9_]{0,63}$')
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."messages" (
	"id" text PRIMARY KEY NOT NULL,
	"session_id" uuid NOT NULL,
	"app_id" text NOT NULL,
	"workspace_id" text NOT NULL,
	"request_id" text,
	"accepted_at" timestamp with time zone NOT NULL,
	CONSTRAINT "messages_id_check" CHECK ("cantelop_observability"."messages"."id" ~ '^msg_[0-9a-f]{32}$'),
	CONSTRAINT "messages_app_id_check" CHECK ("cantelop_observability"."messages"."app_id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "messages_workspace_id_check" CHECK ("cantelop_observability"."messages"."workspace_id" ~ '^wsp_[0-9a-f]{32}$'),
	CONSTRAINT "messages_request_id_check" CHECK ("cantelop_observability"."messages"."request_id" IS NULL OR "cantelop_observability"."messages"."request_id" ~ '^req_[0-9a-f]{32}$')
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."requests" (
	"id" text PRIMARY KEY NOT NULL,
	"app_id" text NOT NULL,
	"workspace_id" text,
	"trace_id" text NOT NULL,
	"method" text,
	"path" text,
	"status_code" integer,
	"duration_us" bigint,
	"outcome" text,
	"received_at" timestamp with time zone NOT NULL,
	"completed_at" timestamp with time zone,
	"incomplete" boolean DEFAULT false NOT NULL,
	CONSTRAINT "requests_id_check" CHECK ("cantelop_observability"."requests"."id" ~ '^req_[0-9a-f]{32}$'),
	CONSTRAINT "requests_app_id_check" CHECK ("cantelop_observability"."requests"."app_id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "requests_workspace_id_check" CHECK ("cantelop_observability"."requests"."workspace_id" IS NULL OR "cantelop_observability"."requests"."workspace_id" ~ '^wsp_[0-9a-f]{32}$'),
	CONSTRAINT "requests_trace_id_check" CHECK ("cantelop_observability"."requests"."trace_id" ~ '^[0-9a-f]{32}$' AND "cantelop_observability"."requests"."trace_id" <> repeat('0', 32)),
	CONSTRAINT "requests_status_code_check" CHECK ("cantelop_observability"."requests"."status_code" IS NULL OR "cantelop_observability"."requests"."status_code" BETWEEN 100 AND 599),
	CONSTRAINT "requests_duration_check" CHECK ("cantelop_observability"."requests"."duration_us" IS NULL OR "cantelop_observability"."requests"."duration_us" >= 0),
	CONSTRAINT "requests_completed_check" CHECK ("cantelop_observability"."requests"."completed_at" IS NULL OR "cantelop_observability"."requests"."completed_at" >= "cantelop_observability"."requests"."received_at")
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."retention_state" (
	"id" integer PRIMARY KEY NOT NULL,
	"last_run_at" timestamp with time zone NOT NULL,
	CONSTRAINT "retention_state_singleton_check" CHECK ("cantelop_observability"."retention_state"."id" = 1)
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."sandboxes" (
	"id" text PRIMARY KEY NOT NULL,
	"session_id" uuid NOT NULL,
	"app_id" text NOT NULL,
	"workspace_id" text NOT NULL,
	"concrete_session_id" text,
	"build_id" text,
	"allocated_at" timestamp with time zone NOT NULL,
	"terminated_at" timestamp with time zone,
	"outcome" text,
	CONSTRAINT "sandboxes_id_check" CHECK ("cantelop_observability"."sandboxes"."id" ~ '^sbx-[0-9a-f]{32}$'),
	CONSTRAINT "sandboxes_app_id_check" CHECK ("cantelop_observability"."sandboxes"."app_id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "sandboxes_workspace_id_check" CHECK ("cantelop_observability"."sandboxes"."workspace_id" ~ '^wsp_[0-9a-f]{32}$'),
	CONSTRAINT "sandboxes_concrete_session_id_check" CHECK ("cantelop_observability"."sandboxes"."concrete_session_id" IS NULL OR "cantelop_observability"."sandboxes"."concrete_session_id" ~ '^ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$'),
	CONSTRAINT "sandboxes_terminated_check" CHECK ("cantelop_observability"."sandboxes"."terminated_at" IS NULL OR "cantelop_observability"."sandboxes"."terminated_at" >= "cantelop_observability"."sandboxes"."allocated_at")
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."sessions" (
	"id" uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
	"app_id" text NOT NULL,
	"workspace_id" text NOT NULL,
	"logical_session_id" text NOT NULL,
	"first_seen_at" timestamp with time zone NOT NULL,
	"last_seen_at" timestamp with time zone NOT NULL,
	CONSTRAINT "sessions_logical_unique" UNIQUE("app_id","workspace_id","logical_session_id"),
	CONSTRAINT "sessions_app_id_check" CHECK ("cantelop_observability"."sessions"."app_id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "sessions_workspace_id_check" CHECK ("cantelop_observability"."sessions"."workspace_id" ~ '^wsp_[0-9a-f]{32}$'),
	CONSTRAINT "sessions_logical_id_check" CHECK ("cantelop_observability"."sessions"."logical_session_id" ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
	CONSTRAINT "sessions_seen_check" CHECK ("cantelop_observability"."sessions"."last_seen_at" >= "cantelop_observability"."sessions"."first_seen_at")
);
--> statement-breakpoint
CREATE TABLE "cantelop_observability"."spans" (
	"id" text PRIMARY KEY NOT NULL,
	"attempt_id" text NOT NULL,
	"trace_id" text NOT NULL,
	"parent_span_id" text,
	"name" text NOT NULL,
	"kind" text NOT NULL,
	"started_at" timestamp with time zone NOT NULL,
	"finished_at" timestamp with time zone,
	"status" text NOT NULL,
	"attributes" jsonb DEFAULT '{}'::jsonb NOT NULL,
	CONSTRAINT "spans_id_check" CHECK ("cantelop_observability"."spans"."id" ~ '^[0-9a-f]{16}$' AND "cantelop_observability"."spans"."id" <> repeat('0', 16)),
	CONSTRAINT "spans_trace_id_check" CHECK ("cantelop_observability"."spans"."trace_id" ~ '^[0-9a-f]{32}$' AND "cantelop_observability"."spans"."trace_id" <> repeat('0', 32)),
	CONSTRAINT "spans_name_check" CHECK (length("cantelop_observability"."spans"."name") BETWEEN 1 AND 256),
	CONSTRAINT "spans_kind_check" CHECK ("cantelop_observability"."spans"."kind" IN ('internal', 'client', 'server')),
	CONSTRAINT "spans_status_check" CHECK ("cantelop_observability"."spans"."status" IN ('unset', 'ok', 'error')),
	CONSTRAINT "spans_finished_check" CHECK ("cantelop_observability"."spans"."finished_at" IS NULL OR "cantelop_observability"."spans"."finished_at" >= "cantelop_observability"."spans"."started_at"),
	CONSTRAINT "spans_attributes_check" CHECK (jsonb_typeof("cantelop_observability"."spans"."attributes") = 'object')
);
--> statement-breakpoint
ALTER TABLE "cantelop_observability"."logs" ADD CONSTRAINT "logs_session_id_sessions_id_fk" FOREIGN KEY ("session_id") REFERENCES "cantelop_observability"."sessions"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."logs" ADD CONSTRAINT "logs_sandbox_id_sandboxes_id_fk" FOREIGN KEY ("sandbox_id") REFERENCES "cantelop_observability"."sandboxes"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."logs" ADD CONSTRAINT "logs_attempt_id_message_attempts_id_fk" FOREIGN KEY ("attempt_id") REFERENCES "cantelop_observability"."message_attempts"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."logs" ADD CONSTRAINT "logs_span_id_spans_id_fk" FOREIGN KEY ("span_id") REFERENCES "cantelop_observability"."spans"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."message_attempts" ADD CONSTRAINT "message_attempts_message_id_messages_id_fk" FOREIGN KEY ("message_id") REFERENCES "cantelop_observability"."messages"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."message_attempts" ADD CONSTRAINT "message_attempts_sandbox_id_sandboxes_id_fk" FOREIGN KEY ("sandbox_id") REFERENCES "cantelop_observability"."sandboxes"("id") ON DELETE set null ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."messages" ADD CONSTRAINT "messages_session_id_sessions_id_fk" FOREIGN KEY ("session_id") REFERENCES "cantelop_observability"."sessions"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."sandboxes" ADD CONSTRAINT "sandboxes_session_id_sessions_id_fk" FOREIGN KEY ("session_id") REFERENCES "cantelop_observability"."sessions"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."spans" ADD CONSTRAINT "spans_attempt_id_message_attempts_id_fk" FOREIGN KEY ("attempt_id") REFERENCES "cantelop_observability"."message_attempts"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_observability"."spans" ADD CONSTRAINT "spans_parent_span_id_spans_id_fk" FOREIGN KEY ("parent_span_id") REFERENCES "cantelop_observability"."spans"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "ingestion_receipts_received" ON "cantelop_observability"."ingestion_receipts" USING btree ("received_at","source","event_id");--> statement-breakpoint
CREATE INDEX "logs_attempt_occurred" ON "cantelop_observability"."logs" USING btree ("attempt_id","occurred_at","id");--> statement-breakpoint
CREATE INDEX "logs_span_occurred" ON "cantelop_observability"."logs" USING btree ("span_id","occurred_at","id");--> statement-breakpoint
CREATE INDEX "logs_sandbox_occurred" ON "cantelop_observability"."logs" USING btree ("sandbox_id","occurred_at","id");--> statement-breakpoint
CREATE INDEX "message_attempts_sandbox_queued" ON "cantelop_observability"."message_attempts" USING btree ("sandbox_id","queued_at","id");--> statement-breakpoint
CREATE INDEX "message_attempts_app_queued" ON "cantelop_observability"."message_attempts" USING btree ("app_id","queued_at","id");--> statement-breakpoint
CREATE INDEX "messages_session_accepted" ON "cantelop_observability"."messages" USING btree ("session_id","accepted_at","id");--> statement-breakpoint
CREATE INDEX "messages_request" ON "cantelop_observability"."messages" USING btree ("request_id","id");--> statement-breakpoint
CREATE INDEX "requests_app_received" ON "cantelop_observability"."requests" USING btree ("app_id","received_at","id");--> statement-breakpoint
CREATE INDEX "sandboxes_session_allocated" ON "cantelop_observability"."sandboxes" USING btree ("session_id","allocated_at","id");--> statement-breakpoint
CREATE INDEX "sandboxes_app_allocated" ON "cantelop_observability"."sandboxes" USING btree ("app_id","allocated_at","id");--> statement-breakpoint
CREATE INDEX "sessions_app_seen" ON "cantelop_observability"."sessions" USING btree ("app_id","last_seen_at","id");--> statement-breakpoint
CREATE INDEX "spans_attempt_started" ON "cantelop_observability"."spans" USING btree ("attempt_id","started_at","id");--> statement-breakpoint
CREATE INDEX "spans_trace_started" ON "cantelop_observability"."spans" USING btree ("trace_id","started_at","id");
--> statement-breakpoint
REVOKE ALL ON ALL TABLES IN SCHEMA "cantelop_observability" FROM PUBLIC;
