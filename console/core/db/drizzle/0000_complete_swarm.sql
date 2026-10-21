CREATE TABLE "cantelop_control"."api_artifacts" (
	"id" text PRIMARY KEY NOT NULL,
	"digest" text NOT NULL,
	"object_key" text NOT NULL,
	"size_bytes" integer NOT NULL,
	"manifest" jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "api_artifacts_digest_unique" UNIQUE("digest"),
	CONSTRAINT "api_artifacts_object_key_unique" UNIQUE("object_key"),
	CONSTRAINT "api_artifacts_id_check" CHECK ("cantelop_control"."api_artifacts"."id" ~ '^art_[0-9a-f]{32}$'),
	CONSTRAINT "api_artifacts_digest_check" CHECK ("cantelop_control"."api_artifacts"."digest" ~ '^sha256:[0-9a-f]{64}$'),
	CONSTRAINT "api_artifacts_object_key_check" CHECK ("cantelop_control"."api_artifacts"."object_key" ~ '^deployment-uploads/[0-9a-f]{32}/api/worker\.mjs$'),
	CONSTRAINT "api_artifacts_size_bytes_check" CHECK ("cantelop_control"."api_artifacts"."size_bytes" BETWEEN 1 AND 1048576),
	CONSTRAINT "api_artifacts_manifest_check" CHECK (jsonb_typeof("cantelop_control"."api_artifacts"."manifest") = 'object')
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."app_config_values" (
	"app_id" text NOT NULL,
	"name" text NOT NULL,
	"kind" text NOT NULL,
	"value_text" text,
	"secret_nonce" "bytea",
	"secret_ciphertext" "bytea",
	"value_bytes" integer NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "app_config_values_app_id_name_pk" PRIMARY KEY("app_id","name"),
	CONSTRAINT "app_config_values_name_check" CHECK ("cantelop_control"."app_config_values"."name" ~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$' AND "cantelop_control"."app_config_values"."name" !~ '^CANTELOP_'),
	CONSTRAINT "app_config_values_kind_check" CHECK ("cantelop_control"."app_config_values"."kind" IN ('variable', 'secret')),
	CONSTRAINT "app_config_values_value_bytes_check" CHECK ("cantelop_control"."app_config_values"."value_bytes" BETWEEN 0 AND 65536),
	CONSTRAINT "app_config_values_shape_check" CHECK (("cantelop_control"."app_config_values"."kind" = 'variable' AND "cantelop_control"."app_config_values"."value_text" IS NOT NULL AND "cantelop_control"."app_config_values"."secret_nonce" IS NULL AND "cantelop_control"."app_config_values"."secret_ciphertext" IS NULL) OR ("cantelop_control"."app_config_values"."kind" = 'secret' AND "cantelop_control"."app_config_values"."value_text" IS NULL AND octet_length("cantelop_control"."app_config_values"."secret_nonce") = 12 AND octet_length("cantelop_control"."app_config_values"."secret_ciphertext") >= 16))
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."app_harnesses" (
	"app_id" text PRIMARY KEY NOT NULL,
	"internal_port" integer NOT NULL,
	"max_sessions" integer NOT NULL,
	"runtime_ttl_seconds" integer NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "app_harnesses_internal_port_check" CHECK ("cantelop_control"."app_harnesses"."internal_port" BETWEEN 1 AND 65535),
	CONSTRAINT "app_harnesses_max_sessions_check" CHECK ("cantelop_control"."app_harnesses"."max_sessions" BETWEEN 1 AND 64),
	CONSTRAINT "app_harnesses_runtime_ttl_seconds_check" CHECK ("cantelop_control"."app_harnesses"."runtime_ttl_seconds" BETWEEN 30 AND 604800),
	CONSTRAINT "app_harnesses_updated_at_check" CHECK ("cantelop_control"."app_harnesses"."updated_at" >= "cantelop_control"."app_harnesses"."created_at")
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."apps" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_user_id" text NOT NULL,
	"slug" text NOT NULL,
	"status" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"archived_at" timestamp with time zone,
	"active_release_id" text,
	"config_revision" bigint DEFAULT 0 NOT NULL,
	CONSTRAINT "apps_id_check" CHECK ("cantelop_control"."apps"."id" ~ '^app_[0-9a-f]{32}$'),
	CONSTRAINT "apps_slug_check" CHECK ("cantelop_control"."apps"."slug" ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND position('--' IN "cantelop_control"."apps"."slug") = 0),
	CONSTRAINT "apps_status_check" CHECK ("cantelop_control"."apps"."status" IN ('provisioning', 'active', 'archived')),
	CONSTRAINT "apps_updated_at_check" CHECK ("cantelop_control"."apps"."updated_at" >= "cantelop_control"."apps"."created_at"),
	CONSTRAINT "apps_archived_at_check" CHECK (("cantelop_control"."apps"."status" = 'archived') = ("cantelop_control"."apps"."archived_at" IS NOT NULL)),
	CONSTRAINT "apps_config_revision_check" CHECK ("cantelop_control"."apps"."config_revision" >= 0)
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."deployment_uploads" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_user_id" text NOT NULL,
	"app_id" text NOT NULL,
	"idempotency_key" text NOT NULL,
	"status" text NOT NULL,
	"api_digest" text NOT NULL,
	"api_object_key" text NOT NULL,
	"api_size_bytes" integer NOT NULL,
	"api_manifest" jsonb NOT NULL,
	"harness_digest" text NOT NULL,
	"harness_object_key" text NOT NULL,
	"harness_size_bytes" bigint NOT NULL,
	"harness_media_type" text NOT NULL,
	"harness_store_upload_id" text,
	"harness_part_size_bytes" integer,
	"harness_part_count" integer,
	"release_id" text,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "deployment_uploads_release_id_unique" UNIQUE("release_id"),
	CONSTRAINT "deployment_uploads_owner_idempotency_unique" UNIQUE("owner_user_id","idempotency_key"),
	CONSTRAINT "deployment_uploads_id_check" CHECK ("cantelop_control"."deployment_uploads"."id" ~ '^upl_[0-9a-f]{32}$'),
	CONSTRAINT "deployment_uploads_idempotency_key_check" CHECK (length("cantelop_control"."deployment_uploads"."idempotency_key") BETWEEN 16 AND 128 AND "cantelop_control"."deployment_uploads"."idempotency_key" ~ '^[A-Za-z0-9_.:-]+$'),
	CONSTRAINT "deployment_uploads_status_check" CHECK ("cantelop_control"."deployment_uploads"."status" IN ('preparing', 'uploading', 'finalized', 'expired')),
	CONSTRAINT "deployment_uploads_api_digest_check" CHECK ("cantelop_control"."deployment_uploads"."api_digest" ~ '^sha256:[0-9a-f]{64}$'),
	CONSTRAINT "deployment_uploads_api_object_key_check" CHECK ("cantelop_control"."deployment_uploads"."api_object_key" ~ '^deployment-uploads/[0-9a-f]{32}/api/worker\.mjs$'),
	CONSTRAINT "deployment_uploads_api_size_bytes_check" CHECK ("cantelop_control"."deployment_uploads"."api_size_bytes" BETWEEN 1 AND 1048576),
	CONSTRAINT "deployment_uploads_api_manifest_check" CHECK (jsonb_typeof("cantelop_control"."deployment_uploads"."api_manifest") = 'object'),
	CONSTRAINT "deployment_uploads_harness_digest_check" CHECK ("cantelop_control"."deployment_uploads"."harness_digest" ~ '^sha256:[0-9a-f]{64}$'),
	CONSTRAINT "deployment_uploads_harness_object_key_check" CHECK ("cantelop_control"."deployment_uploads"."harness_object_key" ~ '^deployment-uploads/[0-9a-f]{32}/harness/image\.tar\.gz$'),
	CONSTRAINT "deployment_uploads_harness_size_bytes_check" CHECK ("cantelop_control"."deployment_uploads"."harness_size_bytes" BETWEEN 1 AND 21474836480),
	CONSTRAINT "deployment_uploads_harness_media_type_check" CHECK ("cantelop_control"."deployment_uploads"."harness_media_type" = 'application/vnd.cantelop.harness.image.v1.tar+gzip'),
	CONSTRAINT "deployment_uploads_harness_store_upload_id_check" CHECK ("cantelop_control"."deployment_uploads"."harness_store_upload_id" IS NULL OR (length("cantelop_control"."deployment_uploads"."harness_store_upload_id") BETWEEN 1 AND 1024 AND "cantelop_control"."deployment_uploads"."harness_store_upload_id" !~ '[\x00\r\n]')),
	CONSTRAINT "deployment_uploads_harness_part_size_check" CHECK ("cantelop_control"."deployment_uploads"."harness_part_size_bytes" IS NULL OR "cantelop_control"."deployment_uploads"."harness_part_size_bytes" BETWEEN 5242880 AND 536870912),
	CONSTRAINT "deployment_uploads_harness_part_count_check" CHECK ("cantelop_control"."deployment_uploads"."harness_part_count" IS NULL OR "cantelop_control"."deployment_uploads"."harness_part_count" BETWEEN 1 AND 10000),
	CONSTRAINT "deployment_uploads_preparing_check" CHECK ("cantelop_control"."deployment_uploads"."status" <> 'preparing' OR ("cantelop_control"."deployment_uploads"."harness_store_upload_id" IS NULL AND "cantelop_control"."deployment_uploads"."harness_part_size_bytes" IS NULL AND "cantelop_control"."deployment_uploads"."harness_part_count" IS NULL)),
	CONSTRAINT "deployment_uploads_uploaded_check" CHECK ("cantelop_control"."deployment_uploads"."status" NOT IN ('uploading', 'finalized') OR ("cantelop_control"."deployment_uploads"."harness_store_upload_id" IS NOT NULL AND "cantelop_control"."deployment_uploads"."harness_part_size_bytes" IS NOT NULL AND "cantelop_control"."deployment_uploads"."harness_part_count" IS NOT NULL)),
	CONSTRAINT "deployment_uploads_updated_at_check" CHECK ("cantelop_control"."deployment_uploads"."updated_at" >= "cantelop_control"."deployment_uploads"."created_at"),
	CONSTRAINT "deployment_uploads_finalized_check" CHECK (("cantelop_control"."deployment_uploads"."status" = 'finalized') = ("cantelop_control"."deployment_uploads"."release_id" IS NOT NULL)),
	CONSTRAINT "deployment_uploads_expiry_check" CHECK ("cantelop_control"."deployment_uploads"."expires_at" > "cantelop_control"."deployment_uploads"."created_at")
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."workspaces" (
	"id" text PRIMARY KEY NOT NULL,
	"app_id" text NOT NULL,
	"slug" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"archived_at" timestamp with time zone,
	CONSTRAINT "workspaces_app_slug_unique" UNIQUE("app_id","slug"),
	CONSTRAINT "workspaces_id_check" CHECK ("cantelop_control"."workspaces"."id" ~ '^wsp_[0-9a-f]{32}$'),
	CONSTRAINT "workspaces_slug_check" CHECK ("cantelop_control"."workspaces"."slug" ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND position('--' IN "cantelop_control"."workspaces"."slug") = 0),
	CONSTRAINT "workspaces_updated_at_check" CHECK ("cantelop_control"."workspaces"."updated_at" >= "cantelop_control"."workspaces"."created_at")
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."harness_artifacts" (
	"id" text PRIMARY KEY NOT NULL,
	"digest" text NOT NULL,
	"object_key" text NOT NULL,
	"size_bytes" bigint NOT NULL,
	"media_type" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "harness_artifacts_object_key_unique" UNIQUE("object_key"),
	CONSTRAINT "harness_artifacts_id_check" CHECK ("cantelop_control"."harness_artifacts"."id" ~ '^har_[0-9a-f]{32}$'),
	CONSTRAINT "harness_artifacts_digest_check" CHECK ("cantelop_control"."harness_artifacts"."digest" ~ '^sha256:[0-9a-f]{64}$'),
	CONSTRAINT "harness_artifacts_object_key_check" CHECK ("cantelop_control"."harness_artifacts"."object_key" ~ '^deployment-uploads/[0-9a-f]{32}/harness/image\.tar\.gz$'),
	CONSTRAINT "harness_artifacts_size_bytes_check" CHECK ("cantelop_control"."harness_artifacts"."size_bytes" BETWEEN 1 AND 21474836480),
	CONSTRAINT "harness_artifacts_media_type_check" CHECK ("cantelop_control"."harness_artifacts"."media_type" = 'application/vnd.cantelop.harness.image.v1.tar+gzip')
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."operations" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_user_id" text NOT NULL,
	"app_id" text NOT NULL,
	"kind" text NOT NULL,
	"status" text NOT NULL,
	"idempotency_key" text NOT NULL,
	"input" jsonb NOT NULL,
	"output" jsonb,
	"error_code" text,
	"attempts" integer DEFAULT 0 NOT NULL,
	"available_at" timestamp with time zone NOT NULL,
	"locked_by" text,
	"locked_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "operations_owner_idempotency_unique" UNIQUE("owner_user_id","idempotency_key"),
	CONSTRAINT "operations_id_check" CHECK ("cantelop_control"."operations"."id" ~ '^op_[0-9a-f]{32}$'),
	CONSTRAINT "operations_kind_check" CHECK ("cantelop_control"."operations"."kind" IN ('register_app', 'register_workspace', 'sync_config', 'deploy_api', 'materialize_build', 'archive_app')),
	CONSTRAINT "operations_status_check" CHECK ("cantelop_control"."operations"."status" IN ('queued', 'running', 'succeeded', 'failed')),
	CONSTRAINT "operations_idempotency_key_check" CHECK (length("cantelop_control"."operations"."idempotency_key") BETWEEN 16 AND 128 AND "cantelop_control"."operations"."idempotency_key" ~ '^[A-Za-z0-9_.:-]+$'),
	CONSTRAINT "operations_input_check" CHECK (jsonb_typeof("cantelop_control"."operations"."input") = 'object'),
	CONSTRAINT "operations_output_check" CHECK ("cantelop_control"."operations"."output" IS NULL OR jsonb_typeof("cantelop_control"."operations"."output") = 'object'),
	CONSTRAINT "operations_error_code_check" CHECK ("cantelop_control"."operations"."error_code" IS NULL OR "cantelop_control"."operations"."error_code" ~ '^[a-z][a-z0-9_]{0,63}$'),
	CONSTRAINT "operations_attempts_check" CHECK ("cantelop_control"."operations"."attempts" BETWEEN 0 AND 100),
	CONSTRAINT "operations_updated_at_check" CHECK ("cantelop_control"."operations"."updated_at" >= "cantelop_control"."operations"."created_at"),
	CONSTRAINT "operations_running_check" CHECK (("cantelop_control"."operations"."status" = 'running') = ("cantelop_control"."operations"."locked_by" IS NOT NULL AND "cantelop_control"."operations"."locked_at" IS NOT NULL)),
	CONSTRAINT "operations_succeeded_check" CHECK (("cantelop_control"."operations"."status" = 'succeeded') = ("cantelop_control"."operations"."output" IS NOT NULL)),
	CONSTRAINT "operations_failed_check" CHECK (("cantelop_control"."operations"."status" = 'failed') = ("cantelop_control"."operations"."error_code" IS NOT NULL))
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."release_components" (
	"release_id" text NOT NULL,
	"kind" text NOT NULL,
	"operation_id" text NOT NULL,
	"status" text NOT NULL,
	"output" jsonb,
	"error_code" text,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "release_components_release_id_kind_pk" PRIMARY KEY("release_id","kind"),
	CONSTRAINT "release_components_operation_id_unique" UNIQUE("operation_id"),
	CONSTRAINT "release_components_kind_check" CHECK ("cantelop_control"."release_components"."kind" IN ('api', 'harness')),
	CONSTRAINT "release_components_status_check" CHECK ("cantelop_control"."release_components"."status" IN ('queued', 'running', 'succeeded', 'failed')),
	CONSTRAINT "release_components_output_check" CHECK ("cantelop_control"."release_components"."output" IS NULL OR jsonb_typeof("cantelop_control"."release_components"."output") = 'object'),
	CONSTRAINT "release_components_error_code_check" CHECK ("cantelop_control"."release_components"."error_code" IS NULL OR "cantelop_control"."release_components"."error_code" ~ '^[a-z][a-z0-9_]{0,63}$'),
	CONSTRAINT "release_components_updated_at_check" CHECK ("cantelop_control"."release_components"."updated_at" >= "cantelop_control"."release_components"."created_at"),
	CONSTRAINT "release_components_succeeded_check" CHECK (("cantelop_control"."release_components"."status" = 'succeeded') = ("cantelop_control"."release_components"."output" IS NOT NULL)),
	CONSTRAINT "release_components_failed_check" CHECK (("cantelop_control"."release_components"."status" = 'failed') = ("cantelop_control"."release_components"."error_code" IS NOT NULL))
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."releases" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_user_id" text NOT NULL,
	"app_id" text NOT NULL,
	"api_artifact_id" text NOT NULL,
	"harness_artifact_id" text NOT NULL,
	"version" integer NOT NULL,
	"status" text NOT NULL,
	"idempotency_key" text NOT NULL,
	"error_code" text,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"activated_at" timestamp with time zone,
	CONSTRAINT "releases_app_version_unique" UNIQUE("app_id","version"),
	CONSTRAINT "releases_owner_idempotency_unique" UNIQUE("owner_user_id","idempotency_key"),
	CONSTRAINT "releases_id_check" CHECK ("cantelop_control"."releases"."id" ~ '^rel_[0-9a-f]{32}$'),
	CONSTRAINT "releases_version_check" CHECK ("cantelop_control"."releases"."version" > 0),
	CONSTRAINT "releases_status_check" CHECK ("cantelop_control"."releases"."status" IN ('queued', 'provisioning', 'active', 'failed', 'superseded')),
	CONSTRAINT "releases_idempotency_key_check" CHECK (length("cantelop_control"."releases"."idempotency_key") BETWEEN 16 AND 128 AND "cantelop_control"."releases"."idempotency_key" ~ '^[A-Za-z0-9_.:-]+$'),
	CONSTRAINT "releases_error_code_check" CHECK ("cantelop_control"."releases"."error_code" IS NULL OR "cantelop_control"."releases"."error_code" ~ '^[a-z][a-z0-9_]{0,63}$'),
	CONSTRAINT "releases_updated_at_check" CHECK ("cantelop_control"."releases"."updated_at" >= "cantelop_control"."releases"."created_at"),
	CONSTRAINT "releases_failed_check" CHECK (("cantelop_control"."releases"."status" = 'failed') = ("cantelop_control"."releases"."error_code" IS NOT NULL)),
	CONSTRAINT "releases_activated_check" CHECK (("cantelop_control"."releases"."status" IN ('active', 'superseded')) = ("cantelop_control"."releases"."activated_at" IS NOT NULL))
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."user_quotas" (
	"user_id" text PRIMARY KEY NOT NULL,
	"max_apps" integer NOT NULL,
	"max_sessions" integer NOT NULL,
	"max_vcpus" integer NOT NULL,
	"max_memory_mib" bigint NOT NULL,
	CONSTRAINT "user_quotas_max_apps_check" CHECK ("cantelop_control"."user_quotas"."max_apps" BETWEEN 1 AND 10000),
	CONSTRAINT "user_quotas_max_sessions_check" CHECK ("cantelop_control"."user_quotas"."max_sessions" BETWEEN 1 AND 100000),
	CONSTRAINT "user_quotas_max_vcpus_check" CHECK ("cantelop_control"."user_quotas"."max_vcpus" BETWEEN 1 AND 1000000),
	CONSTRAINT "user_quotas_max_memory_mib_check" CHECK ("cantelop_control"."user_quotas"."max_memory_mib" BETWEEN 1 AND 1073741824)
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."users" (
	"id" text PRIMARY KEY NOT NULL,
	"provider" text NOT NULL,
	"provider_subject" text NOT NULL,
	"email" text NOT NULL,
	"status" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"last_login_at" timestamp with time zone NOT NULL,
	CONSTRAINT "users_provider_subject_unique" UNIQUE("provider","provider_subject"),
	CONSTRAINT "users_id_check" CHECK ("cantelop_control"."users"."id" ~ '^usr_[0-9a-f]{32}$'),
	CONSTRAINT "users_provider_check" CHECK ("cantelop_control"."users"."provider" ~ '^[a-z][a-z0-9_-]{0,31}$'),
	CONSTRAINT "users_provider_subject_check" CHECK ("cantelop_control"."users"."provider_subject" ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$'),
	CONSTRAINT "users_email_check" CHECK (length("cantelop_control"."users"."email") <= 320 AND "cantelop_control"."users"."email" = btrim("cantelop_control"."users"."email")),
	CONSTRAINT "users_status_check" CHECK ("cantelop_control"."users"."status" IN ('active', 'suspended')),
	CONSTRAINT "users_last_login_check" CHECK ("cantelop_control"."users"."last_login_at" >= "cantelop_control"."users"."created_at")
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_config_values" ADD CONSTRAINT "app_config_values_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_harnesses" ADD CONSTRAINT "app_harnesses_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."apps" ADD CONSTRAINT "apps_owner_user_id_users_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."apps" ADD CONSTRAINT "apps_active_release_id_releases_id_fk" FOREIGN KEY ("active_release_id") REFERENCES "cantelop_control"."releases"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_owner_user_id_users_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."deployment_uploads" ADD CONSTRAINT "deployment_uploads_release_id_releases_id_fk" FOREIGN KEY ("release_id") REFERENCES "cantelop_control"."releases"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."workspaces" ADD CONSTRAINT "workspaces_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."operations" ADD CONSTRAINT "operations_owner_user_id_users_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."operations" ADD CONSTRAINT "operations_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."release_components" ADD CONSTRAINT "release_components_release_id_releases_id_fk" FOREIGN KEY ("release_id") REFERENCES "cantelop_control"."releases"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."release_components" ADD CONSTRAINT "release_components_operation_id_operations_id_fk" FOREIGN KEY ("operation_id") REFERENCES "cantelop_control"."operations"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" ADD CONSTRAINT "releases_owner_user_id_users_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" ADD CONSTRAINT "releases_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" ADD CONSTRAINT "releases_api_artifact_id_api_artifacts_id_fk" FOREIGN KEY ("api_artifact_id") REFERENCES "cantelop_control"."api_artifacts"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."releases" ADD CONSTRAINT "releases_harness_artifact_id_harness_artifacts_id_fk" FOREIGN KEY ("harness_artifact_id") REFERENCES "cantelop_control"."harness_artifacts"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."user_quotas" ADD CONSTRAINT "user_quotas_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "app_config_values_app" ON "cantelop_control"."app_config_values" USING btree ("app_id","name");--> statement-breakpoint
CREATE UNIQUE INDEX "apps_routable_slug" ON "cantelop_control"."apps" USING btree ("slug") WHERE "cantelop_control"."apps"."status" <> 'archived';--> statement-breakpoint
CREATE INDEX "apps_owner" ON "cantelop_control"."apps" USING btree ("owner_user_id","created_at","id");--> statement-breakpoint
CREATE INDEX "deployment_uploads_expiry" ON "cantelop_control"."deployment_uploads" USING btree ("expires_at","id") WHERE "cantelop_control"."deployment_uploads"."status" = 'uploading';--> statement-breakpoint
CREATE UNIQUE INDEX "deployment_uploads_pending_app" ON "cantelop_control"."deployment_uploads" USING btree ("app_id") WHERE "cantelop_control"."deployment_uploads"."status" IN ('preparing', 'uploading');--> statement-breakpoint
CREATE INDEX "harness_artifacts_digest" ON "cantelop_control"."harness_artifacts" USING btree ("digest");--> statement-breakpoint
CREATE INDEX "operations_ready" ON "cantelop_control"."operations" USING btree ("available_at","created_at","id") WHERE "cantelop_control"."operations"."status" = 'queued';--> statement-breakpoint
CREATE INDEX "operations_app" ON "cantelop_control"."operations" USING btree ("app_id","created_at","id");--> statement-breakpoint
CREATE INDEX "release_components_status" ON "cantelop_control"."release_components" USING btree ("status","updated_at","release_id");--> statement-breakpoint
CREATE UNIQUE INDEX "releases_pending_app" ON "cantelop_control"."releases" USING btree ("app_id") WHERE "cantelop_control"."releases"."status" IN ('queued', 'provisioning');--> statement-breakpoint
CREATE UNIQUE INDEX "releases_active_app" ON "cantelop_control"."releases" USING btree ("app_id") WHERE "cantelop_control"."releases"."status" = 'active';--> statement-breakpoint
CREATE INDEX "releases_app" ON "cantelop_control"."releases" USING btree ("app_id","version");--> statement-breakpoint
CREATE INDEX "workspaces_app" ON "cantelop_control"."workspaces" USING btree ("app_id","created_at","id");
--> statement-breakpoint
REVOKE ALL ON SCHEMA "cantelop_control" FROM PUBLIC;
--> statement-breakpoint
REVOKE ALL ON ALL TABLES IN SCHEMA "cantelop_control" FROM PUBLIC;
