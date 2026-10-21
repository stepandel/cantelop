import { controlDatabase, type SQLClient } from "./database";

export interface AdminCounts {
  users: number;
  apps: number;
}

// Platform-wide totals, not scoped to the operator's own apps. Scalar
// subqueries avoid multiplying the counts through a users/apps join.
export async function getAdminCounts(client: SQLClient = controlDatabase()): Promise<AdminCounts> {
  const { rows: [row] } = await client.query<{ users: string; apps: string }>(`
    SELECT
      (SELECT count(*) FROM cantelop_control.users) AS users,
      (SELECT count(*) FROM cantelop_control.apps WHERE status <> 'archived') AS apps
  `);
  return { users: Number(row.users), apps: Number(row.apps) };
}
