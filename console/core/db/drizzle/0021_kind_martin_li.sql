CREATE TABLE "cantelop_control"."demo_visitor_request_budgets" (
	"visitor_id" text PRIMARY KEY NOT NULL,
	"day_started_at" timestamp with time zone NOT NULL,
	"day_count" integer NOT NULL,
	"minute_started_at" timestamp with time zone NOT NULL,
	"minute_count" integer NOT NULL,
	CONSTRAINT "demo_visitor_request_budgets_day_count_check" CHECK ("cantelop_control"."demo_visitor_request_budgets"."day_count" BETWEEN 1 AND 50),
	CONSTRAINT "demo_visitor_request_budgets_minute_count_check" CHECK ("cantelop_control"."demo_visitor_request_budgets"."minute_count" BETWEEN 1 AND 10)
);
