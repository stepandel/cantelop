import type { SQLPool } from "../db/database";

/** Daily cleanup excludes active invitations and retains suppression fingerprints. */
export async function purgeExpiredInvitations(database: SQLPool, now = new Date()): Promise<number> {
  const cutoff = new Date(now.getTime() - 90 * 86_400_000);
  const result = await database.query(`DELETE FROM cantelop_control.app_invitations
WHERE id IN (SELECT id FROM cantelop_control.app_invitations WHERE expires_at < $1 ORDER BY expires_at LIMIT 1000)`, [cutoff]);
  return result.rowCount ?? 0;
}
