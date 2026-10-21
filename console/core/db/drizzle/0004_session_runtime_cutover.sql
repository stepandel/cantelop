ALTER TABLE "cantelop_control"."app_harnesses" RENAME TO "app_session_runtimes";--> statement-breakpoint
ALTER TABLE "cantelop_control"."harness_artifacts" RENAME TO "session_runtime_artifacts";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_digest" TO "session_runtime_digest";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_object_key" TO "session_runtime_object_key";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_size_bytes" TO "session_runtime_size_bytes";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_media_type" TO "session_runtime_media_type";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_store_upload_id" TO "session_runtime_store_upload_id";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_part_size_bytes" TO "session_runtime_part_size_bytes";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" RENAME COLUMN "harness_part_count" TO "session_runtime_part_count";--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" RENAME COLUMN "harness_artifact_id" TO "session_runtime_artifact_id";--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" DROP CONSTRAINT "harness_artifacts_object_key_unique";--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_session_runtimes" DROP CONSTRAINT "app_harnesses_internal_port_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_session_runtimes" DROP CONSTRAINT "app_harnesses_updated_at_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_digest_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_object_key_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_size_bytes_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_media_type_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_store_upload_id_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_part_size_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_harness_part_count_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_preparing_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" DROP CONSTRAINT "deployment_uploads_uploaded_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" DROP CONSTRAINT "harness_artifacts_id_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" DROP CONSTRAINT "harness_artifacts_digest_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" DROP CONSTRAINT "harness_artifacts_object_key_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" DROP CONSTRAINT "harness_artifacts_size_bytes_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" DROP CONSTRAINT "harness_artifacts_media_type_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."release_components" DROP CONSTRAINT "release_components_kind_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_session_runtimes" DROP CONSTRAINT "app_harnesses_app_id_apps_id_fk";
--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" DROP CONSTRAINT "releases_harness_artifact_id_harness_artifacts_id_fk";
--> statement-breakpoint
DROP INDEX "cantelop_control"."harness_artifacts_digest";--> statement-breakpoint
UPDATE "cantelop_control"."deployment_uploads"
SET "session_runtime_object_key" = replace("session_runtime_object_key", '/harness/', '/session-runtime/'),
    "session_runtime_media_type" = 'application/vnd.cantelop.session-runtime.image.v1.tar+gzip';--> statement-breakpoint
UPDATE "cantelop_control"."session_runtime_artifacts"
SET "id" = 'sra_' || substring("id" from 5),
    "object_key" = replace("object_key", '/harness/', '/session-runtime/'),
    "media_type" = 'application/vnd.cantelop.session-runtime.image.v1.tar+gzip';--> statement-breakpoint
UPDATE "cantelop_control"."releases"
SET "session_runtime_artifact_id" = 'sra_' || substring("session_runtime_artifact_id" from 5);--> statement-breakpoint
UPDATE "cantelop_control"."operations"
SET "input" = jsonb_set(
  jsonb_set("input", '{artifact_id}', to_jsonb('sra_' || substring("input"->>'artifact_id' from 5))),
  '{object_key}', to_jsonb(replace("input"->>'object_key', '/harness/', '/session-runtime/'))
)
WHERE "kind" = 'materialize_build';--> statement-breakpoint
UPDATE "cantelop_control"."release_components"
SET "kind" = 'session_runtime'
WHERE "kind" = 'harness';--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_session_runtimes" ADD CONSTRAINT "app_session_runtimes_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" ADD CONSTRAINT "releases_session_runtime_artifact_id_session_runtime_artifacts_id_fk" FOREIGN KEY ("session_runtime_artifact_id") REFERENCES "cantelop_control"."session_runtime_artifacts"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "session_runtime_artifacts_digest" ON "cantelop_control"."session_runtime_artifacts" USING btree ("digest");--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" ADD CONSTRAINT "session_runtime_artifacts_object_key_unique" UNIQUE("object_key");--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_session_runtimes" ADD CONSTRAINT "app_session_runtimes_internal_port_check" CHECK ("cantelop_control"."app_session_runtimes"."internal_port" BETWEEN 1 AND 65535);--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_session_runtimes" ADD CONSTRAINT "app_session_runtimes_updated_at_check" CHECK ("cantelop_control"."app_session_runtimes"."updated_at" >= "cantelop_control"."app_session_runtimes"."created_at");--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_digest_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_digest" ~ '^sha256:[0-9a-f]{64}$');--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_object_key_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_object_key" ~ '^deployment-uploads/[0-9a-f]{32}/session-runtime/image\.tar\.gz$');--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_size_bytes_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_size_bytes" BETWEEN 1 AND 21474836480);--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_media_type_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_media_type" = 'application/vnd.cantelop.session-runtime.image.v1.tar+gzip');--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_store_upload_id_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_store_upload_id" IS NULL OR (length("cantelop_control"."deployment_uploads"."session_runtime_store_upload_id") BETWEEN 1 AND 1024 AND "cantelop_control"."deployment_uploads"."session_runtime_store_upload_id" !~ '[\x00\r\n]'));--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_part_size_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_part_size_bytes" IS NULL OR "cantelop_control"."deployment_uploads"."session_runtime_part_size_bytes" BETWEEN 5242880 AND 536870912);--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_session_runtime_part_count_check" CHECK ("cantelop_control"."deployment_uploads"."session_runtime_part_count" IS NULL OR "cantelop_control"."deployment_uploads"."session_runtime_part_count" BETWEEN 1 AND 10000);--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_preparing_check" CHECK ("cantelop_control"."deployment_uploads"."status" <> 'preparing' OR ("cantelop_control"."deployment_uploads"."session_runtime_store_upload_id" IS NULL AND "cantelop_control"."deployment_uploads"."session_runtime_part_size_bytes" IS NULL AND "cantelop_control"."deployment_uploads"."session_runtime_part_count" IS NULL));--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_uploaded_check" CHECK ("cantelop_control"."deployment_uploads"."status" NOT IN ('uploading', 'finalized') OR ("cantelop_control"."deployment_uploads"."session_runtime_store_upload_id" IS NOT NULL AND "cantelop_control"."deployment_uploads"."session_runtime_part_size_bytes" IS NOT NULL AND "cantelop_control"."deployment_uploads"."session_runtime_part_count" IS NOT NULL));--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" ADD CONSTRAINT "session_runtime_artifacts_id_check" CHECK ("cantelop_control"."session_runtime_artifacts"."id" ~ '^sra_[0-9a-f]{32}$');--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" ADD CONSTRAINT "session_runtime_artifacts_digest_check" CHECK ("cantelop_control"."session_runtime_artifacts"."digest" ~ '^sha256:[0-9a-f]{64}$');--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" ADD CONSTRAINT "session_runtime_artifacts_object_key_check" CHECK ("cantelop_control"."session_runtime_artifacts"."object_key" ~ '^deployment-uploads/[0-9a-f]{32}/session-runtime/image\.tar\.gz$');--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" ADD CONSTRAINT "session_runtime_artifacts_size_bytes_check" CHECK ("cantelop_control"."session_runtime_artifacts"."size_bytes" BETWEEN 1 AND 21474836480);--> statement-breakpoint
ALTER TABLE "cantelop_control"."session_runtime_artifacts" ADD CONSTRAINT "session_runtime_artifacts_media_type_check" CHECK ("cantelop_control"."session_runtime_artifacts"."media_type" = 'application/vnd.cantelop.session-runtime.image.v1.tar+gzip');--> statement-breakpoint
ALTER TABLE "cantelop_control"."release_components" ADD CONSTRAINT "release_components_kind_check" CHECK ("cantelop_control"."release_components"."kind" IN ('api', 'session_runtime'));
