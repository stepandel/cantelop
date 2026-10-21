import "server-only";

import { controlDatabase, type SQLClient } from "@core/db/database";

/** Every demo visitor uses the same atomic browser-identity budget. */
export async function consumeDemoRequestBudget(
  visitorID: string, database: SQLClient = controlDatabase(),
): Promise<boolean> {
  if (!/^visitor:[a-f0-9]{32}$/.test(visitorID)) throw new TypeError("Invalid demo visitor");
  const result = await database.query<{ visitor_id: string }>(`INSERT INTO cantelop_control.demo_visitor_request_budgets
  (visitor_id, day_started_at, day_count, minute_started_at, minute_count)
VALUES ($1, now(), 1, now(), 1)
ON CONFLICT (visitor_id) DO UPDATE SET
  day_started_at = CASE WHEN demo_visitor_request_budgets.day_started_at <= now() - interval '1 day'
    THEN now() ELSE demo_visitor_request_budgets.day_started_at END,
  day_count = CASE WHEN demo_visitor_request_budgets.day_started_at <= now() - interval '1 day'
    THEN 1 ELSE demo_visitor_request_budgets.day_count + 1 END,
  minute_started_at = CASE WHEN demo_visitor_request_budgets.minute_started_at <= now() - interval '1 minute'
    THEN now() ELSE demo_visitor_request_budgets.minute_started_at END,
  minute_count = CASE WHEN demo_visitor_request_budgets.minute_started_at <= now() - interval '1 minute'
    THEN 1 ELSE demo_visitor_request_budgets.minute_count + 1 END
WHERE (demo_visitor_request_budgets.day_started_at <= now() - interval '1 day' OR demo_visitor_request_budgets.day_count < 50)
  AND (demo_visitor_request_budgets.minute_started_at <= now() - interval '1 minute' OR demo_visitor_request_budgets.minute_count < 10)
RETURNING visitor_id`, [visitorID]);
  return result.rowCount === 1;
}
