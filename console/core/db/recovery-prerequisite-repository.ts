import { ControlError } from "../domain/errors";
import { assertResourceID, newResourceID } from "../domain/ids";
import { withTransaction, type SQLPool, type TransactionClient } from "./database";

export type RecoveryReadiness = "ready" | "registration_pending" | "registration_failed" | "configuration_pending" | "configuration_failed";

// Recover desired state through the ordinary workers, preserving the failed
// operations as evidence. Keys are scoped to the recovery and config revision,
// so retries and worker restarts cannot enqueue duplicate repairs.
export class RecoveryPrerequisiteRepository {
  constructor(private readonly database: SQLPool) {}

  async prepare(operationID: string, workerID: string): Promise<RecoveryReadiness> {
    assertResourceID("operation", operationID);
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid recovery worker ID");
    return withTransaction(this.database, async (client) => {
      const identity = await client.query<{ app_id: string; owner_user_id: string }>(
        `SELECT app_id, owner_user_id FROM cantelop_control.operations WHERE id = $1 AND kind = 'cancel_release'`, [operationID]);
      const owner = identity.rows[0];
      if (!owner) throw new ControlError("not_found");
      // Match recovery's App -> operation lock order and never resurrect an
      // archived App or use a stale operation's ownership/configuration.
      const desired = await client.query<{ slug: string; internal_port: number; config_revision: string }>(
        `SELECT a.slug, sr.internal_port, a.config_revision
FROM cantelop_control.apps a JOIN cantelop_control.app_session_runtimes sr ON sr.app_id = a.id
JOIN cantelop_control.users u ON u.id = a.owner_user_id
WHERE a.id = $1 AND a.owner_user_id = $2 AND a.status <> 'archived' AND u.status = 'active'
FOR UPDATE OF a`, [owner.app_id, owner.owner_user_id]);
      const app = desired.rows[0];
      if (!app) throw new ControlError("not_found");
      const claimed = await client.query<{ input: { restart: boolean } }>(
        `SELECT input FROM cantelop_control.operations
WHERE id = $1 AND kind = 'cancel_release' AND status = 'running' AND locked_by = $2 FOR UPDATE`, [operationID, workerID]);
      if (!claimed.rows[0]) throw new ControlError("conflict");

      // A completed registration must not be replayed: its provisioning shape
      // has no Build, while a deployed App may now have an active Build. Wait
      // for in-flight registration, and repair only failed or absent work.
      const latest = await client.query<{ status: string }>(`SELECT status FROM cantelop_control.operations
WHERE app_id = $1 AND owner_user_id = $2 AND kind = 'register_app'
ORDER BY created_at DESC, id DESC LIMIT 1`, [owner.app_id, owner.owner_user_id]);
      let registration = latest.rows[0]?.status;
      if (!registration || registration === "failed") {
        registration = await this.ensure(client, owner, `${operationID}:registration`, "register_app", {
          app_slug: app.slug, internal_port: app.internal_port,
        });
      }
      let readiness: RecoveryReadiness = registration === "succeeded" ? "ready"
        : registration === "failed" ? "registration_failed" : "registration_pending";
      const revision = Number(app.config_revision);
      if (readiness === "ready" && claimed.rows[0].input.restart && revision > 0) {
        const configuration = await this.ensure(client, owner, `${operationID}:configuration:${revision}`, "sync_config", { revision });
        readiness = configuration === "succeeded" ? "ready"
          : configuration === "failed" ? "configuration_failed" : "configuration_pending";
      }
      return readiness;
    });
  }

  private async ensure(client: TransactionClient, owner: { owner_user_id: string; app_id: string },
    key: string, kind: "register_app" | "sync_config", input: Record<string, unknown>): Promise<string> {
    await client.query(`INSERT INTO cantelop_control.operations
(id, owner_user_id, app_id, kind, status, idempotency_key, input, available_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'queued', $5, $6::jsonb, clock_timestamp(), clock_timestamp(), clock_timestamp())
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING`, [
      newResourceID("operation"), owner.owner_user_id, owner.app_id, kind, key, JSON.stringify(input),
    ]);
    const result = await client.query<{ status: string }>(`SELECT status FROM cantelop_control.operations
WHERE owner_user_id = $1 AND app_id = $2 AND kind = $3 AND idempotency_key = $4 AND input = $5::jsonb`, [
      owner.owner_user_id, owner.app_id, kind, key, JSON.stringify(input),
    ]);
    if (!result.rows[0]) throw new ControlError("conflict");
    return result.rows[0].status;
  }
}
