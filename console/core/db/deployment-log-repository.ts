import { ControlError } from "../domain/errors";
import { assertResourceID } from "../domain/ids";
import type { SQLClient } from "./database";

export interface DeploymentLog {
  id: string;
  created_at: string;
  source: "deployment" | "api" | "session_runtime";
  level: "info" | "warning" | "error";
  message: string;
}

export class DeploymentLogRepository {
  constructor(private readonly database: SQLClient) {}

  async listForUser(ownerID: string, appID: string, releaseID: string): Promise<{ items: DeploymentLog[]; truncated: boolean }> {
    assertResourceID("user", ownerID);
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    const owned = await this.database.query(`SELECT id FROM cantelop_control.releases
WHERE id = $1 AND app_id = $2 AND owner_user_id = $3`, [releaseID, appID, ownerID]);
    if (owned.rowCount !== 1) throw new ControlError("not_found");
    // Recheck ownership in the log query. Read a bounded tail on each refresh;
    // identity cursors alone could skip concurrently committing transactions.
    const result = await this.database.query<DeploymentLog & Record<string, unknown>>(`SELECT l.id::text, l.created_at, l.source, l.level, l.message
FROM cantelop_control.deployment_logs l
JOIN cantelop_control.releases r ON r.id = l.release_id
WHERE r.id = $1 AND r.app_id = $2 AND r.owner_user_id = $3
ORDER BY l.id DESC LIMIT 2001`, [releaseID, appID, ownerID]);
    return {
      items: result.rows.slice(0, 2000).reverse().map(row => ({ ...row, created_at: new Date(row.created_at).toISOString() })),
      truncated: result.rows.length > 2000,
    };
  }
}
