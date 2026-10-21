import { ControlError } from "../domain/errors";
import { assertResourceID } from "../domain/ids";
import type { SQLPool } from "./database";

const workerIDPattern = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/;

export class ComputeRegistrationRepository {
  constructor(private readonly database: SQLPool, private readonly now: () => Date = () => new Date()) {}

  async succeedWorkspace(operationID: string, workerID: string, workspaceID: string): Promise<void> {
    assertResourceID("operation", operationID);
    assertResourceID("workspace", workspaceID);
    if (!workerIDPattern.test(workerID)) throw new TypeError("invalid Workspace registration completion");
    const operation = await this.database.query(`UPDATE cantelop_control.operations
SET status = 'succeeded', output = jsonb_build_object('provider_workspace_id', $3::text),
    locked_by = NULL, locked_at = NULL, updated_at = $4
WHERE id = $1 AND kind = 'register_workspace' AND status = 'running' AND locked_by = $2
RETURNING id`, [operationID, workerID, workspaceID, this.now()]);
    if (operation.rowCount !== 1) throw new ControlError("conflict");
  }

  async workspaceRegistrationContext(appID: string, workspaceID: string): Promise<{
    archived: boolean; computeTenantID: string;
  } | null> {
    assertResourceID("app", appID);
    assertResourceID("workspace", workspaceID);
    const result = await this.database.query<{ archived: boolean; compute_tenant_id: string }>(`SELECT
  (w.archived_at IS NOT NULL OR a.status='archived') AS archived,
  coalesce(a.compute_tenant_id,a.owner_user_id) AS compute_tenant_id
FROM cantelop_control.workspaces w
JOIN cantelop_control.apps a ON a.id=w.app_id
WHERE w.id=$1 AND w.app_id=$2`, [workspaceID, appID]);
    const row=result.rows[0];
    if(!row) return null;
    assertResourceID("user", row.compute_tenant_id);
    return {archived:row.archived,computeTenantID:row.compute_tenant_id};
  }
}
