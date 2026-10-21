import { ControlError } from "../domain/errors";
import { assertResourceID } from "../domain/ids";
import type { SQLClient } from "./database";

export type ComputeTenantResolver = (appID: string, ownerUserID: string) => Promise<string>;

/** Private provider addressing only: never use this opaque key to authorize a
 * customer. Product ownership and membership remain authoritative for access. */
export function computeTenantResolver(database: SQLClient): ComputeTenantResolver {
  return async (appID, _ownerUserID) => {
    assertResourceID("app", appID);
    const result = await database.query<{ tenant_id: string }>(`SELECT coalesce(compute_tenant_id, owner_user_id) AS tenant_id
FROM cantelop_control.apps WHERE id = $1`, [appID]);
    if (!result.rows[0]) throw new ControlError("not_found");
    assertResourceID("user", result.rows[0].tenant_id);
    return result.rows[0].tenant_id;
  };
}
