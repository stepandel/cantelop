CREATE TABLE "cantelop_control"."user_identities" (
	"user_id" text NOT NULL,
	"provider" text NOT NULL,
	"provider_subject" text NOT NULL,
	"email" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"last_login_at" timestamp with time zone NOT NULL,
	CONSTRAINT "user_identities_provider_provider_subject_pk" PRIMARY KEY("provider","provider_subject"),
	CONSTRAINT "user_identities_provider_check" CHECK ("cantelop_control"."user_identities"."provider" ~ '^[a-z][a-z0-9_-]{0,31}$'),
	CONSTRAINT "user_identities_provider_subject_check" CHECK ("cantelop_control"."user_identities"."provider_subject" ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$'),
	CONSTRAINT "user_identities_email_check" CHECK (length("cantelop_control"."user_identities"."email") <= 320 AND "cantelop_control"."user_identities"."email" = lower(btrim("cantelop_control"."user_identities"."email") COLLATE "C")),
	CONSTRAINT "user_identities_last_login_check" CHECK ("cantelop_control"."user_identities"."last_login_at" >= "cantelop_control"."user_identities"."created_at")
);
--> statement-breakpoint
-- Only Google ever vouched for an address before this migration; a GitHub
-- login stored whatever profile address the API returned, verified or not.
-- Merge on verified addresses alone. Every other user stays separate under a
-- placeholder address until their next sign-in reports a verified one.
CREATE TEMPORARY TABLE "cantelop_user_merge" ON COMMIT DROP AS
SELECT "id" AS "old_user_id",
	first_value("id") OVER (
		PARTITION BY CASE WHEN "provider" = 'google' AND "email" <> ''
			THEN 'email:' || lower("email" COLLATE "C") ELSE 'user:' || "id" END
		ORDER BY "created_at", "id"
	) AS "user_id"
FROM "cantelop_control"."users";
--> statement-breakpoint
ALTER TABLE "cantelop_control"."user_identities" ADD CONSTRAINT "user_identities_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE cascade ON UPDATE no action;
--> statement-breakpoint
INSERT INTO "cantelop_control"."user_identities"
	("user_id", "provider", "provider_subject", "email", "created_at", "last_login_at")
SELECT merge."user_id", users."provider", users."provider_subject", lower(users."email" COLLATE "C"), users."created_at", users."last_login_at"
FROM "cantelop_control"."users" users
JOIN "cantelop_user_merge" merge ON merge."old_user_id" = users."id";
--> statement-breakpoint
CREATE TEMPORARY TABLE "cantelop_merged_collaborators" ON COMMIT DROP AS
SELECT collaborators."app_id", member_merge."user_id", 'admin'::text AS "role",
	(array_agg(added_by_merge."user_id" ORDER BY collaborators."created_at", collaborators."added_by_user_id"))[1] AS "added_by_user_id",
	min(collaborators."created_at") AS "created_at"
FROM "cantelop_control"."app_collaborators" collaborators
JOIN "cantelop_user_merge" member_merge ON member_merge."old_user_id" = collaborators."user_id"
JOIN "cantelop_user_merge" added_by_merge ON added_by_merge."old_user_id" = collaborators."added_by_user_id"
JOIN "cantelop_control"."apps" apps ON apps."id" = collaborators."app_id"
JOIN "cantelop_user_merge" owner_merge ON owner_merge."old_user_id" = apps."owner_user_id"
WHERE member_merge."user_id" <> owner_merge."user_id"
GROUP BY collaborators."app_id", member_merge."user_id";
--> statement-breakpoint
UPDATE "cantelop_control"."user_quotas" quotas
SET "max_apps" = merged."max_apps", "max_vcpus" = merged."max_vcpus", "max_memory_mib" = merged."max_memory_mib"
FROM (
	SELECT merge."user_id", max(quotas."max_apps") AS "max_apps", max(quotas."max_vcpus") AS "max_vcpus",
		max(quotas."max_memory_mib") AS "max_memory_mib"
	FROM "cantelop_control"."user_quotas" quotas
	JOIN "cantelop_user_merge" merge ON merge."old_user_id" = quotas."user_id"
	GROUP BY merge."user_id"
) merged
WHERE quotas."user_id" = merged."user_id";
--> statement-breakpoint
DELETE FROM "cantelop_control"."user_quotas" quotas
USING "cantelop_user_merge" merge
WHERE quotas."user_id" = merge."old_user_id" AND merge."old_user_id" <> merge."user_id";
--> statement-breakpoint
WITH duplicate_keys AS (
	SELECT operations."id", row_number() OVER (
		PARTITION BY merge."user_id", operations."idempotency_key" ORDER BY operations."created_at", operations."id"
	) AS duplicate_number
	FROM "cantelop_control"."operations" operations
	JOIN "cantelop_user_merge" merge ON merge."old_user_id" = operations."owner_user_id"
)
UPDATE "cantelop_control"."operations" operations
SET "idempotency_key" = left(operations."idempotency_key", 94) || ':' || substring(operations."id" FROM 4)
FROM duplicate_keys
WHERE operations."id" = duplicate_keys."id" AND duplicate_keys.duplicate_number > 1;
--> statement-breakpoint
WITH duplicate_keys AS (
	SELECT releases."id", row_number() OVER (
		PARTITION BY merge."user_id", releases."idempotency_key" ORDER BY releases."created_at", releases."id"
	) AS duplicate_number
	FROM "cantelop_control"."releases" releases
	JOIN "cantelop_user_merge" merge ON merge."old_user_id" = releases."owner_user_id"
)
UPDATE "cantelop_control"."releases" releases
SET "idempotency_key" = left(releases."idempotency_key", 94) || ':' || substring(releases."id" FROM 5)
FROM duplicate_keys
WHERE releases."id" = duplicate_keys."id" AND duplicate_keys.duplicate_number > 1;
--> statement-breakpoint
WITH duplicate_keys AS (
	SELECT uploads."id", row_number() OVER (
		PARTITION BY merge."user_id", uploads."idempotency_key" ORDER BY uploads."created_at", uploads."id"
	) AS duplicate_number
	FROM "cantelop_control"."deployment_uploads" uploads
	JOIN "cantelop_user_merge" merge ON merge."old_user_id" = uploads."owner_user_id"
)
UPDATE "cantelop_control"."deployment_uploads" uploads
SET "idempotency_key" = left(uploads."idempotency_key", 94) || ':' || substring(uploads."id" FROM 5)
FROM duplicate_keys
WHERE uploads."id" = duplicate_keys."id" AND duplicate_keys.duplicate_number > 1;
--> statement-breakpoint
DELETE FROM "cantelop_control"."app_collaborators";
--> statement-breakpoint
UPDATE "cantelop_control"."apps" resources SET "owner_user_id" = merge."user_id"
FROM "cantelop_user_merge" merge WHERE resources."owner_user_id" = merge."old_user_id";
--> statement-breakpoint
UPDATE "cantelop_control"."operations" resources SET "owner_user_id" = merge."user_id"
FROM "cantelop_user_merge" merge WHERE resources."owner_user_id" = merge."old_user_id";
--> statement-breakpoint
UPDATE "cantelop_control"."releases" resources SET "owner_user_id" = merge."user_id"
FROM "cantelop_user_merge" merge WHERE resources."owner_user_id" = merge."old_user_id";
--> statement-breakpoint
UPDATE "cantelop_control"."deployment_uploads" resources SET "owner_user_id" = merge."user_id"
FROM "cantelop_user_merge" merge WHERE resources."owner_user_id" = merge."old_user_id";
--> statement-breakpoint
INSERT INTO "cantelop_control"."app_collaborators" ("app_id", "user_id", "role", "added_by_user_id", "created_at")
SELECT "app_id", "user_id", "role", "added_by_user_id", "created_at" FROM "cantelop_merged_collaborators";
--> statement-breakpoint
UPDATE "cantelop_control"."users" users
SET "email" = merged."email", "status" = merged."status", "created_at" = merged."created_at", "last_login_at" = merged."last_login_at"
FROM (
	SELECT merge."user_id",
		CASE WHEN bool_or(users."provider" = 'google' AND users."email" <> '')
			THEN lower(min(users."email") COLLATE "C")
			ELSE lower(min(users."provider" || '-' || users."provider_subject") COLLATE "C") || '@unverified.invalid'
		END AS "email",
		CASE WHEN bool_or(users."status" = 'suspended') THEN 'suspended' ELSE 'active' END AS "status",
		min(users."created_at") AS "created_at", max(users."last_login_at") AS "last_login_at"
	FROM "cantelop_control"."users" users
	JOIN "cantelop_user_merge" merge ON merge."old_user_id" = users."id"
	GROUP BY merge."user_id"
) merged
WHERE users."id" = merged."user_id";
--> statement-breakpoint
DELETE FROM "cantelop_control"."users" users
USING "cantelop_user_merge" merge
WHERE users."id" = merge."old_user_id" AND merge."old_user_id" <> merge."user_id";
--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" DROP CONSTRAINT "users_provider_subject_unique";--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" DROP CONSTRAINT "users_provider_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" DROP CONSTRAINT "users_provider_subject_check";--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" DROP CONSTRAINT "users_email_check";--> statement-breakpoint
CREATE INDEX "user_identities_user" ON "cantelop_control"."user_identities" USING btree ("user_id");--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" DROP COLUMN "provider";--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" DROP COLUMN "provider_subject";--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" ADD CONSTRAINT "users_email_unique" UNIQUE("email");--> statement-breakpoint
ALTER TABLE "cantelop_control"."users" ADD CONSTRAINT "users_email_check" CHECK (length("cantelop_control"."users"."email") <= 320 AND "cantelop_control"."users"."email" = lower(btrim("cantelop_control"."users"."email") COLLATE "C"));
