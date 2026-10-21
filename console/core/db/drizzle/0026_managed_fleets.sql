CREATE TABLE "cantelop_control"."fleet_audit" (
	"sequence" bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY (sequence name "cantelop_control"."fleet_audit_sequence_seq" INCREMENT BY 1 MINVALUE 1 MAXVALUE 9223372036854775807 START WITH 1 CACHE 1),
	"fleet_id" text NOT NULL,
	"actor_id" text NOT NULL,
	"action" text NOT NULL,
	"target" text NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_budgets" (
	"app_id" text NOT NULL,
	"epoch" text NOT NULL,
	"included" bigint NOT NULL,
	"settled" bigint DEFAULT 0 NOT NULL,
	"reserved" bigint DEFAULT 0 NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	CONSTRAINT "fleet_budgets_app_id_epoch_pk" PRIMARY KEY("app_id","epoch"),
	CONSTRAINT "fleet_budget_conservation" CHECK ("cantelop_control"."fleet_budgets"."included" >= 0 AND "cantelop_control"."fleet_budgets"."settled" >= 0 AND "cantelop_control"."fleet_budgets"."reserved" >= 0 AND "cantelop_control"."fleet_budgets"."settled"+"cantelop_control"."fleet_budgets"."reserved" <= "cantelop_control"."fleet_budgets"."included")
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_installations" (
	"id" text PRIMARY KEY NOT NULL,
	"fleet_id" text NOT NULL,
	"external_key" text NOT NULL,
	"app_id" text NOT NULL,
	"package_id" text NOT NULL,
	"release_id" text,
	"desired_state" text DEFAULT 'ready' NOT NULL,
	"error_code" text,
	"lease_until" timestamp with time zone,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "fleet_installations_app_id_unique" UNIQUE("app_id"),
	CONSTRAINT "fleet_installation_external_key" UNIQUE("fleet_id","external_key"),
	CONSTRAINT "fleet_installation_desired" CHECK ("cantelop_control"."fleet_installations"."desired_state" IN ('ready','suspended','deleted'))
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_packages" (
	"id" text PRIMARY KEY NOT NULL,
	"fleet_id" text NOT NULL,
	"name" text NOT NULL,
	"version" text NOT NULL,
	"runtime_id" text NOT NULL,
	"api_artifact_id" text NOT NULL,
	"runtime_artifact_id" text NOT NULL,
	"disabled_at" timestamp with time zone,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "fleet_package_version" UNIQUE("fleet_id","name","version")
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_policies" (
	"app_id" text PRIMARY KEY NOT NULL,
	"revision" integer DEFAULT 1 NOT NULL,
	"policy" jsonb NOT NULL,
	"enforced_revision" integer DEFAULT 0 NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_principals" (
	"id" text PRIMARY KEY NOT NULL,
	"fleet_id" text NOT NULL,
	"label" text NOT NULL,
	"secret_hash" text NOT NULL,
	"scopes" jsonb NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"revoked_at" timestamp with time zone,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_reservations" (
	"id" text PRIMARY KEY NOT NULL,
	"app_id" text NOT NULL,
	"epoch" text NOT NULL,
	"host_id" text NOT NULL,
	"credits" bigint NOT NULL,
	"consumed" bigint,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "fleet_reservation_credit" CHECK ("cantelop_control"."fleet_reservations"."credits" > 0 AND ("cantelop_control"."fleet_reservations"."consumed" IS NULL OR "cantelop_control"."fleet_reservations"."consumed" BETWEEN 0 AND "cantelop_control"."fleet_reservations"."credits"))
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleet_usage" (
	"sequence" bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY (sequence name "cantelop_control"."fleet_usage_sequence_seq" INCREMENT BY 1 MINVALUE 1 MAXVALUE 9223372036854775807 START WITH 1 CACHE 1),
	"id" text NOT NULL,
	"fleet_id" text NOT NULL,
	"app_id" text NOT NULL,
	"reservation_id" text NOT NULL,
	"credits" bigint NOT NULL,
	"observed_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "fleet_usage_id_unique" UNIQUE("id"),
	CONSTRAINT "fleet_usage_reservation_id_unique" UNIQUE("reservation_id")
);
--> statement-breakpoint
CREATE TABLE "cantelop_control"."fleets" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_user_id" text NOT NULL,
	"name" text NOT NULL,
	"max_apps" integer NOT NULL,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "fleets_capacity" CHECK ("cantelop_control"."fleets"."max_apps" BETWEEN 1 AND 10000)
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_audit" ADD CONSTRAINT "fleet_audit_fleet_id_fleets_id_fk" FOREIGN KEY ("fleet_id") REFERENCES "cantelop_control"."fleets"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_budgets" ADD CONSTRAINT "fleet_budgets_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_installations" ADD CONSTRAINT "fleet_installations_fleet_id_fleets_id_fk" FOREIGN KEY ("fleet_id") REFERENCES "cantelop_control"."fleets"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_installations" ADD CONSTRAINT "fleet_installations_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_installations" ADD CONSTRAINT "fleet_installations_package_id_fleet_packages_id_fk" FOREIGN KEY ("package_id") REFERENCES "cantelop_control"."fleet_packages"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_installations" ADD CONSTRAINT "fleet_installations_release_id_releases_id_fk" FOREIGN KEY ("release_id") REFERENCES "cantelop_control"."releases"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD CONSTRAINT "fleet_packages_fleet_id_fleets_id_fk" FOREIGN KEY ("fleet_id") REFERENCES "cantelop_control"."fleets"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD CONSTRAINT "fleet_packages_api_artifact_id_api_artifacts_id_fk" FOREIGN KEY ("api_artifact_id") REFERENCES "cantelop_control"."api_artifacts"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_packages" ADD CONSTRAINT "fleet_packages_runtime_artifact_id_session_runtime_artifacts_id_fk" FOREIGN KEY ("runtime_artifact_id") REFERENCES "cantelop_control"."session_runtime_artifacts"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_policies" ADD CONSTRAINT "fleet_policies_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_principals" ADD CONSTRAINT "fleet_principals_fleet_id_fleets_id_fk" FOREIGN KEY ("fleet_id") REFERENCES "cantelop_control"."fleets"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_reservations" ADD CONSTRAINT "fleet_reservations_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_usage" ADD CONSTRAINT "fleet_usage_fleet_id_fleets_id_fk" FOREIGN KEY ("fleet_id") REFERENCES "cantelop_control"."fleets"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_usage" ADD CONSTRAINT "fleet_usage_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleet_usage" ADD CONSTRAINT "fleet_usage_reservation_id_fleet_reservations_id_fk" FOREIGN KEY ("reservation_id") REFERENCES "cantelop_control"."fleet_reservations"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleets" ADD CONSTRAINT "fleets_owner_user_id_users_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "fleet_principals_fleet" ON "cantelop_control"."fleet_principals" USING btree ("fleet_id");--> statement-breakpoint
CREATE INDEX "fleet_reservations_app" ON "cantelop_control"."fleet_reservations" USING btree ("app_id","epoch");--> statement-breakpoint
CREATE INDEX "fleet_usage_cursor" ON "cantelop_control"."fleet_usage" USING btree ("fleet_id","sequence");