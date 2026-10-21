ALTER TABLE "cantelop_control"."operations" DROP CONSTRAINT "operations_kind_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."operations" DROP CONSTRAINT "operations_attempts_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."operations" ADD CONSTRAINT "operations_kind_check" CHECK ("cantelop_control"."operations"."kind" IN ('register_app', 'register_workspace', 'archive_workspace', 'purge_workspace', 'sync_config', 'deploy_api', 'materialize_build', 'activate_release', 'archive_app', 'cancel_release'));--> statement-breakpoint
ALTER TABLE "cantelop_control"."operations" ADD CONSTRAINT "operations_attempts_check" CHECK ("cantelop_control"."operations"."attempts" >= 0 AND ("cantelop_control"."operations"."kind" IN ('cancel_release', 'activate_release', 'archive_app') OR "cantelop_control"."operations"."attempts" <= 100));--> statement-breakpoint
CREATE SEQUENCE "cantelop_control"."release_activation_generation_seq" AS bigint;
