ALTER TABLE "cantelop_control"."app_harnesses" DROP CONSTRAINT "app_harnesses_max_sessions_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."user_quotas" DROP CONSTRAINT "user_quotas_max_sessions_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_harnesses" DROP COLUMN "max_sessions";--> statement-breakpoint
ALTER TABLE "cantelop_control"."user_quotas" DROP COLUMN "max_sessions";