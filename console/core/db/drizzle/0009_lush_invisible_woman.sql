CREATE TABLE "cantelop_control"."app_collaborators" (
	"app_id" text NOT NULL,
	"user_id" text NOT NULL,
	"role" text NOT NULL,
	"added_by_user_id" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "app_collaborators_app_id_user_id_pk" PRIMARY KEY("app_id","user_id"),
	CONSTRAINT "app_collaborators_role_check" CHECK ("cantelop_control"."app_collaborators"."role" = 'admin')
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_collaborators" ADD CONSTRAINT "app_collaborators_app_id_apps_id_fk" FOREIGN KEY ("app_id") REFERENCES "cantelop_control"."apps"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_collaborators" ADD CONSTRAINT "app_collaborators_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "cantelop_control"."app_collaborators" ADD CONSTRAINT "app_collaborators_added_by_user_id_users_id_fk" FOREIGN KEY ("added_by_user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "app_collaborators_user" ON "cantelop_control"."app_collaborators" USING btree ("user_id","created_at","app_id");