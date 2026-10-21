CREATE INDEX "logs_session" ON "cantelop_observability"."logs" USING btree ("session_id");--> statement-breakpoint
CREATE INDEX "logs_occurred" ON "cantelop_observability"."logs" USING btree ("occurred_at");--> statement-breakpoint
CREATE INDEX "messages_accepted" ON "cantelop_observability"."messages" USING btree ("accepted_at");--> statement-breakpoint
CREATE INDEX "requests_received" ON "cantelop_observability"."requests" USING btree ("received_at");--> statement-breakpoint
CREATE INDEX "sandboxes_allocated" ON "cantelop_observability"."sandboxes" USING btree ("allocated_at");--> statement-breakpoint
CREATE INDEX "sessions_last_seen" ON "cantelop_observability"."sessions" USING btree ("last_seen_at");