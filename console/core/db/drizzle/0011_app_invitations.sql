CREATE TABLE "cantelop_control"."app_invitations" (
	"id" text PRIMARY KEY NOT NULL,
	"app_id" text NOT NULL,
	"email" text NOT NULL,
	"invited_by_user_id" text NOT NULL,
	"token_hash" text NOT NULL,
	"state" text NOT NULL,
	"delivery" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"accepted_by_user_id" text,
	CONSTRAINT "app_invitations_state_check" CHECK ("cantelop_control"."app_invitations"."state" IN ('pending', 'accepted', 'revoked')),
	CONSTRAINT "app_invitations_delivery_check" CHECK ("cantelop_control"."app_invitations"."delivery" IN ('sending', 'sent', 'failed')),
	CONSTRAINT "app_invitations_email_check" CHECK (length("cantelop_control"."app_invitations"."email") BETWEEN 3 AND 320 AND "cantelop_control"."app_invitations"."email" = lower(btrim("cantelop_control"."app_invitations"."email") COLLATE "C")),
	CONSTRAINT "app_invitations_token_check" CHECK ("cantelop_control"."app_invitations"."token_hash" ~ '^[0-9a-f]{64}$'),
	CONSTRAINT "app_invitations_expiry_check" CHECK ("cantelop_control"."app_invitations"."expires_at" > "cantelop_control"."app_invitations"."created_at"),
	CONSTRAINT "app_invitations_acceptance_check" CHECK (("cantelop_control"."app_invitations"."state" = 'accepted') = ("cantelop_control"."app_invitations"."accepted_by_user_id" IS NOT NULL))
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_invitations" ADD CONSTRAINT "app_invitations_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_invitations" ADD CONSTRAINT "app_invitations_invited_by_user_id_users_id_fk" FOREIGN KEY ("invited_by_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_invitations" ADD CONSTRAINT "app_invitations_accepted_by_user_id_users_id_fk" FOREIGN KEY ("accepted_by_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "app_invitations_token" ON "cantelop_control"."app_invitations" USING btree ("token_hash");--> statement-breakpoint
CREATE UNIQUE INDEX "app_invitations_pending_email" ON "cantelop_control"."app_invitations" USING btree ("app_id","email") WHERE "cantelop_control"."app_invitations"."state" = 'pending';--> statement-breakpoint
CREATE INDEX "app_invitations_sender_time" ON "cantelop_control"."app_invitations" USING btree ("invited_by_user_id","created_at");