ALTER TABLE "cantelop_control"."fleets" DROP CONSTRAINT "fleets_capacity";--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleets" ALTER COLUMN "max_apps" DROP NOT NULL;--> statement-breakpoint
ALTER TABLE "cantelop_control"."fleets" ADD CONSTRAINT "fleets_capacity" CHECK ("cantelop_control"."fleets"."max_apps" IS NULL OR "cantelop_control"."fleets"."max_apps" BETWEEN 1 AND 10000);--> statement-breakpoint
-- Fleets now hold an App slot. Raise any quota the new accounting would
-- exceed so no existing account is over quota on deploy.
UPDATE "cantelop_control"."user_quotas" q SET "max_apps" = LEAST(s.apps, 10000) FROM (
  SELECT owner_user_id, count(*)::integer AS apps FROM (
    SELECT a.owner_user_id FROM "cantelop_control"."apps" a
    WHERE a.status <> 'archived'
      AND NOT EXISTS (SELECT 1 FROM "cantelop_control"."fleet_installations" i WHERE i.app_id = a.id)
    UNION ALL
    SELECT f.owner_user_id FROM "cantelop_control"."fleets" f
  ) slots GROUP BY owner_user_id
) s WHERE s.owner_user_id = q.user_id AND s.apps > q.max_apps;