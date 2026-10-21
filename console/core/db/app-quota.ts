import { ControlError } from "../domain/errors";
import type { UserQuota } from "../domain/model";
import type { SQLClient } from "./database";

// A Fleet reserves one slot, including while empty. Its installation Apps are
// already covered by that slot and must never consume standalone App slots.
export const appQuotaUsageSQL = `SELECT count(*)::integer AS apps FROM (
  SELECT a.id FROM cantelop_control.apps a
  WHERE a.owner_user_id = $1 AND a.status <> 'archived'
    AND NOT EXISTS (SELECT 1 FROM cantelop_control.fleet_installations i WHERE i.app_id = a.id)
  UNION ALL
  SELECT f.id FROM cantelop_control.fleets f WHERE f.owner_user_id = $1
) app_slots`;

/** Serialize every new App slot against the same owner and quota rows. */
export async function lockUserAppQuota(client: SQLClient, userID: string): Promise<UserQuota> {
  const result = await client.query<{ status: string; max_apps: number }>(`SELECT u.status, q.max_apps
FROM cantelop_control.users u
JOIN cantelop_control.user_quotas q ON q.user_id = u.id
WHERE u.id = $1
FOR UPDATE OF u, q`, [userID]);
  if (result.rowCount !== 1) throw new ControlError("not_found");
  if (result.rows[0].status !== "active") throw new ControlError("user_suspended");
  return { maxApps: result.rows[0].max_apps };
}

/** Admit an App its Fleet's slot already covers. A shared lock still blocks a
 *  concurrent suspension without serializing installs on the quota row. */
export async function assertActiveAppOwner(client: SQLClient, userID: string): Promise<void> {
  const result = await client.query<{ status: string }>(`SELECT status FROM cantelop_control.users
WHERE id = $1 FOR SHARE`, [userID]);
  if (result.rowCount !== 1) throw new ControlError("not_found");
  if (result.rows[0].status !== "active") throw new ControlError("user_suspended");
}

export async function assertAppQuotaAdmission(client: SQLClient, userID: string): Promise<void> {
  const quota = await lockUserAppQuota(client, userID);
  const usage = await client.query<{ apps: number }>(appQuotaUsageSQL, [userID]);
  if (usage.rows[0].apps >= quota.maxApps) throw new ControlError("quota_exceeded");
}
