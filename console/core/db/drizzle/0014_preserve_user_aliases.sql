CREATE TABLE "cantelop_control"."user_aliases" (
	"source_user_id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "user_aliases_source_check" CHECK ("cantelop_control"."user_aliases"."source_user_id" ~ '^usr_[0-9a-f]{32}$'),
	CONSTRAINT "user_aliases_distinct_check" CHECK ("cantelop_control"."user_aliases"."source_user_id" <> "cantelop_control"."user_aliases"."user_id")
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."user_aliases" ADD CONSTRAINT "user_aliases_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE cascade ON UPDATE no action;
--> statement-breakpoint
CREATE INDEX "user_aliases_user" ON "cantelop_control"."user_aliases" USING btree ("user_id");
