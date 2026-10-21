import { randomBytes } from "node:crypto";
import { normalizeEmail, type IdentityInput } from "../domain/model";
import type { TransactionClient } from "./database";

export function isLegacyPlaceholder(email: string, identity: IdentityInput): boolean {
  return email === normalizeEmail(`${identity.provider}-${identity.providerSubject}@unverified.invalid`);
}

/** Called only after a fresh provider-verified login for the migrated identity.
 * The caller holds the reconciliation advisory lock and source user lock.
 * Keep the account already owning the verified email as the canonical user. */
export async function reconcileLegacyUser(
  client: TransactionClient, sourceID: string, verifiedEmail: string, at: Date,
): Promise<string> {
  const destination = await client.query<{ id: string; status: string }>(`SELECT id, status FROM cantelop_control.users
WHERE email = $1 AND id <> $2 FOR UPDATE`, [verifiedEmail, sourceID]);
  const target = destination.rows[0];
  if (!target) return sourceID;
  // A suspended destination blocks the merge, not this sign-in. The legacy
  // account is active and keeps its own placeholder address; nothing of the
  // suspended account becomes reachable through it.
  if (target.status !== "active") return sourceID;

  await client.query(`SELECT id FROM cantelop_control.apps WHERE owner_user_id IN ($1, $2) ORDER BY id FOR UPDATE`, [sourceID, target.id]);
  const operations = await client.query<{ status: string }>(`SELECT status FROM cantelop_control.operations
WHERE owner_user_id = $1 ORDER BY id FOR UPDATE`, [sourceID]);
  // A claimed worker can still have the old owner in memory. Do not change its
  // product authorization halfway through a build or deployment. Defer the
  // merge to a later sign-in rather than locking the account out of the
  // console for as long as its own deployment runs.
  if (operations.rows.some((operation) => operation.status === "running")) return sourceID;

  await client.query(`INSERT INTO cantelop_control.user_quotas (user_id, max_apps)
SELECT $2, max_apps FROM cantelop_control.user_quotas WHERE user_id = $1
ON CONFLICT (user_id) DO UPDATE SET
max_apps = greatest(cantelop_control.user_quotas.max_apps, EXCLUDED.max_apps)`, [sourceID, target.id]);

  // The provider still owns these Apps under the original opaque tenant ID.
  // Pin it before changing product ownership; all private provider calls resolve
  // this key from the App, including operations claimed before reconciliation.
  await client.query(`UPDATE cantelop_control.apps
SET compute_tenant_id = coalesce(compute_tenant_id, owner_user_id), owner_user_id = $2
WHERE owner_user_id = $1`, [sourceID, target.id]);

  for (const table of ["operations", "releases", "deployment_uploads"] as const) {
    // Each account could legitimately have used the same request key. Keep the
    // canonical account's key and retain every historical source row under a
    // fresh key instead of dropping a release or replaying the wrong operation.
    const collisions = await client.query<{ id: string }>(`SELECT source.id FROM cantelop_control.${table} source
JOIN cantelop_control.${table} target ON target.idempotency_key = source.idempotency_key
WHERE source.owner_user_id = $1 AND target.owner_user_id = $2 ORDER BY source.id FOR UPDATE OF source`, [sourceID, target.id]);
    for (const row of collisions.rows) {
      await client.query(`UPDATE cantelop_control.${table} SET idempotency_key = $2 WHERE id = $1`,
        [row.id, `reconciled:${row.id}:${randomBytes(16).toString("hex")}`]);
    }
    await client.query(`UPDATE cantelop_control.${table} SET owner_user_id = $2 WHERE owner_user_id = $1`, [sourceID, target.id]);
  }

  await client.query(`UPDATE cantelop_control.app_collaborators SET added_by_user_id = $2 WHERE added_by_user_id = $1`, [sourceID, target.id]);
  await client.query(`INSERT INTO cantelop_control.app_collaborators (app_id, user_id, role, added_by_user_id, created_at)
SELECT app_id, $2, role, added_by_user_id, created_at FROM cantelop_control.app_collaborators WHERE user_id = $1
ON CONFLICT (app_id, user_id) DO UPDATE SET created_at = least(cantelop_control.app_collaborators.created_at, EXCLUDED.created_at)`, [sourceID, target.id]);
  await client.query(`DELETE FROM cantelop_control.app_collaborators WHERE user_id = $1`, [sourceID]);
  await client.query(`DELETE FROM cantelop_control.app_collaborators c USING cantelop_control.apps a
WHERE c.app_id = a.id AND c.user_id = $1 AND a.owner_user_id = $1`, [target.id]);

  // Invitations ship independently of account reconciliation. Preserve both
  // references when their additive migration is present during either rollout.
  const invitations = await client.query<{ present: boolean }>("SELECT to_regclass('cantelop_control.app_invitations') IS NOT NULL AS present");
  if (invitations.rows[0].present) {
    await client.query(`UPDATE cantelop_control.app_invitations SET invited_by_user_id = $2 WHERE invited_by_user_id = $1`, [sourceID, target.id]);
    await client.query(`UPDATE cantelop_control.app_invitations SET accepted_by_user_id = $2 WHERE accepted_by_user_id = $1`, [sourceID, target.id]);
  }

  await client.query(`UPDATE cantelop_control.user_identities SET user_id = $2 WHERE user_id = $1`, [sourceID, target.id]);
  await client.query(`UPDATE cantelop_control.users target SET created_at = least(target.created_at, source.created_at),
last_login_at = greatest(target.last_login_at, source.last_login_at, $3)
FROM cantelop_control.users source WHERE target.id = $2 AND source.id = $1`, [sourceID, target.id, at]);
  // Keep signed credentials issued before the merge usable. Flatten existing
  // aliases first so a later merge never leaves a chain or a dangling target.
  await client.query("UPDATE cantelop_control.user_aliases SET user_id = $2 WHERE user_id = $1", [sourceID, target.id]);
  await client.query(`INSERT INTO cantelop_control.user_aliases (source_user_id, user_id, created_at)
VALUES ($1, $2, $3)`, [sourceID, target.id, at]);
  await client.query("DELETE FROM cantelop_control.users WHERE id = $1", [sourceID]);
  return target.id;
}
