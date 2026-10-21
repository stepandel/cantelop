CREATE TABLE "cantelop_control"."workspace_databases" (
	"workspace_id" text PRIMARY KEY NOT NULL,
	"provider_name" text,
	"provider_id" text,
	"url" text,
	"status" text DEFAULT 'pending' NOT NULL,
	"token_nonce" "bytea",
	"token_ciphertext" "bytea",
	"token_expires_at" timestamp with time zone,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL,
	CONSTRAINT "workspace_databases_status_check" CHECK ("cantelop_control"."workspace_databases"."status" IN ('pending', 'ready', 'deleting', 'deleted')),
	CONSTRAINT "workspace_databases_ready_check" CHECK ("cantelop_control"."workspace_databases"."status" <> 'ready' OR ("cantelop_control"."workspace_databases"."provider_name" IS NOT NULL AND "cantelop_control"."workspace_databases"."provider_id" IS NOT NULL AND "cantelop_control"."workspace_databases"."url" IS NOT NULL))
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."workspace_databases" ADD CONSTRAINT "workspace_databases_workspace_id_workspaces_id_fk" FOREIGN KEY ("workspace_id") REFERENCES "cantelop_control"."workspaces"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "workspace_databases_provider_name" ON "cantelop_control"."workspace_databases" USING btree ("provider_name");
--> statement-breakpoint
INSERT INTO cantelop_control.workspace_databases (workspace_id)
SELECT id FROM cantelop_control.workspaces;
--> statement-breakpoint
-- Existing Workspaces use the same durable retry queue as new registrations.
INSERT INTO cantelop_control.operations
(id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
SELECT 'op_' || replace(gen_random_uuid()::text, '-', ''), a.owner_user_id, w.app_id,
'register_workspace', 'queued', 'register-workspace-db:' || w.id,
jsonb_build_object('workspace_id', w.id, 'workspace_slug', w.slug), now(), now(), now()
FROM cantelop_control.workspaces w JOIN cantelop_control.apps a ON a.id = w.app_id
WHERE w.archived_at IS NULL AND a.status <> 'archived'
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;
