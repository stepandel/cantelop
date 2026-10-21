UPDATE "cantelop_control"."operations"
SET "input" = "input" - 'runtime_ttl_seconds'
WHERE "kind" = 'register_app'
  AND "input" ? 'runtime_ttl_seconds';--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_harnesses" DROP CONSTRAINT "app_harnesses_runtime_ttl_seconds_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_harnesses" DROP COLUMN "runtime_ttl_seconds";
