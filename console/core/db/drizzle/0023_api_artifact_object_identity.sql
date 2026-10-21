ALTER TABLE "cantelop_control"."api_artifacts" DROP CONSTRAINT "api_artifacts_digest_unique";--> statement-breakpoint
CREATE INDEX "api_artifacts_digest" ON "cantelop_control"."api_artifacts" USING btree ("digest");--> statement-breakpoint
-- Give each upload-backed release its own API artifact row; digest dedupe bound some releases to another upload's object.
INSERT INTO "cantelop_control"."api_artifacts" ("id", "digest", "object_key", "size_bytes", "manifest", "created_at")
SELECT 'art_' || md5(u."id"), u."api_digest", u."api_object_key", u."api_size_bytes", u."api_manifest", r."created_at"
FROM "cantelop_control"."deployment_uploads" u
JOIN "cantelop_control"."releases" r ON r."id" = u."release_id"
JOIN "cantelop_control"."api_artifacts" a ON a."id" = r."api_artifact_id"
WHERE a."object_key" <> u."api_object_key"
ON CONFLICT DO NOTHING;--> statement-breakpoint
UPDATE "cantelop_control"."releases" r SET "api_artifact_id" = a."id"
FROM "cantelop_control"."deployment_uploads" u
JOIN "cantelop_control"."api_artifacts" a ON a."object_key" = u."api_object_key"
WHERE u."release_id" = r."id" AND r."api_artifact_id" <> a."id";
