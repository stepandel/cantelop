CREATE TABLE "cantelop_control"."app_integration_rotations" (
	"app_id" text PRIMARY KEY NOT NULL,
	"previous_nonce" "bytea",
	"previous_ciphertext" "bytea",
	"previous_expires_at" timestamp with time zone,
	"config_revision" bigint NOT NULL,
	"rotated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "app_integration_rotations_previous_check" CHECK (("cantelop_control"."app_integration_rotations"."previous_nonce" IS NULL AND "cantelop_control"."app_integration_rotations"."previous_ciphertext" IS NULL AND "cantelop_control"."app_integration_rotations"."previous_expires_at" IS NULL) OR (octet_length("cantelop_control"."app_integration_rotations"."previous_nonce") = 12 AND octet_length("cantelop_control"."app_integration_rotations"."previous_ciphertext") >= 16 AND "cantelop_control"."app_integration_rotations"."previous_expires_at" > "cantelop_control"."app_integration_rotations"."rotated_at")),
	CONSTRAINT "app_integration_rotations_revision_check" CHECK ("cantelop_control"."app_integration_rotations"."config_revision" >= 1)
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_integration_rotations" ADD CONSTRAINT "app_integration_rotations_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE cascade ON UPDATE no action;