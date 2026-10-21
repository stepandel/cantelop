CREATE TABLE "cantelop_control"."demo_request_budgets" (
	"user_id" text PRIMARY KEY NOT NULL,
	"day_started_at" timestamp with time zone NOT NULL,
	"day_count" integer NOT NULL,
	"minute_started_at" timestamp with time zone NOT NULL,
	"minute_count" integer NOT NULL,
	CONSTRAINT "demo_request_budgets_day_count_check" CHECK ("cantelop_control"."demo_request_budgets"."day_count" BETWEEN 1 AND 50),
	CONSTRAINT "demo_request_budgets_minute_count_check" CHECK ("cantelop_control"."demo_request_budgets"."minute_count" BETWEEN 1 AND 10)
);
--> statement-breakpoint
ALTER TABLE "cantelop_control"."demo_request_budgets" ADD CONSTRAINT "demo_request_budgets_user_id_users_id_fk" FOREIGN KEY ("user_id") REFERENCES "cantelop_control"."users"("id") ON DELETE cascade ON UPDATE no action;