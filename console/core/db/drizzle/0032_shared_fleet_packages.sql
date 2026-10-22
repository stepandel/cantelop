ALTER TABLE "cantelop_control"."operations" DROP CONSTRAINT "operations_kind_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD COLUMN "prepared_manifest_key" text;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD COLUMN "prepared_manifest_digest" text;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD COLUMN "edge_ready_at" timestamp with time zone;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD COLUMN "edge_retry_at" timestamp with time zone DEFAULT now() NOT NULL;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD COLUMN "edge_compatibility_date" text DEFAULT '2026-08-11' NOT NULL;--> statement-breakpoint
ALTER TABLE "cantelop_control"."operations" ADD CONSTRAINT "operations_kind_check" CHECK ("cantelop_control"."operations"."kind" IN ('register_app', 'register_workspace', 'archive_workspace', 'purge_workspace', 'sync_config', 'deploy_api', 'materialize_build', 'activate_release', 'archive_app', 'cancel_release', 'attach_package'));