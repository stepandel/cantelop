import type { FleetPolicyWorker } from "./policy-worker";
import type { SQLPool } from "../db/database";
import type { ConfigRepository } from "../configuration/repository";
import { ReleaseRepository } from "../db/release-repository";

/** Durable installation intent -> existing release operations. Ambiguous starts replay by key. */
export class FleetInstallationWorker {
  constructor(
    private db: SQLPool,
    private config: ConfigRepository,
    private host?: Pick<FleetPolicyWorker, "call">,
  ) {}
  async runOnce(): Promise<boolean> {
    const claim = await this.db
      .query(`UPDATE cantelop_control.fleet_installations SET lease_until=now()+interval '30 seconds'
      WHERE id=(SELECT f.id FROM cantelop_control.fleet_installations f WHERE f.desired_state='ready'
      AND EXISTS(SELECT 1 FROM cantelop_control.fleet_packages p WHERE p.id=coalesce(f.next_package_id,f.package_id)
        AND (p.prepared_manifest_key IS NULL OR p.edge_ready_at IS NOT NULL))
      AND (f.release_id IS NULL OR f.next_package_id IS NOT NULL
        OR EXISTS(SELECT 1 FROM cantelop_control.releases r WHERE r.id=f.release_id AND r.status='failed' AND r.updated_at<now()-interval '5 minutes'
          AND (SELECT count(*) FROM cantelop_control.releases x WHERE x.app_id=f.app_id AND x.status='failed'
            AND starts_with(x.idempotency_key,'fleet-install:'||f.id||':'||f.package_id))<5))
      AND (f.lease_until IS NULL OR f.lease_until<now()) ORDER BY f.updated_at FOR UPDATE OF f SKIP LOCKED LIMIT 1) RETURNING *`);
    const i = claim.rows[0];
    if (!i) return false;
    try {
      if (i.next_package_id) {
        if (!this.host) throw new Error("managed_drain_unavailable");
        const context = (
          await this.db.query(
            `SELECT coalesce(compute_tenant_id,owner_user_id) AS tenant FROM cantelop_control.apps WHERE id=$1`,
            [i.app_id],
          )
        ).rows[0]!;
        const activity = await this.host.call<{ state: { draining: boolean }; activity: number }>(
          String(context.tenant),
          String(i.app_id),
          "GET",
        );
        if (!activity.state.draining || activity.activity !== 0) return true;
        await this.db.query(
          `UPDATE cantelop_control.fleet_installations SET package_id=next_package_id,next_package_id=NULL,release_id=NULL WHERE id=$1 AND next_package_id=$2`,
          [i.id, i.next_package_id],
        );
        i.package_id = i.next_package_id;
        i.release_id = null;
      }
      const source = await this.db.query(
        `SELECT p.disabled_at,p.prepared_manifest_key,p.edge_ready_at,f.owner_user_id,aa.digest AS api_digest,aa.object_key AS api_key,aa.size_bytes AS api_size,aa.manifest AS api_manifest,
        ra.digest AS runtime_digest,ra.object_key AS runtime_key,ra.size_bytes AS runtime_size,ra.manifest AS runtime_manifest,ra.media_type
        FROM cantelop_control.fleet_packages p JOIN cantelop_control.fleets f ON f.id=p.fleet_id
        JOIN cantelop_control.api_artifacts aa ON aa.id=p.api_artifact_id JOIN cantelop_control.session_runtime_artifacts ra ON ra.id=p.runtime_artifact_id
        WHERE p.id=$1 AND p.fleet_id=$2`,
        [i.package_id, i.fleet_id],
      );
      const p = source.rows[0]!;
      if (p.disabled_at) throw new Error("package_disabled");
      if (p.prepared_manifest_key && !p.edge_ready_at) throw new Error("package_preparing");
      await this.config.preparePackageDeployment(
        String(p.owner_user_id),
        String(i.app_id),
        String(i.package_id),
      );
      const release = await new ReleaseRepository(this.db).begin(
        String(p.owner_user_id),
        String(i.app_id),
        {
          digest: String(p.api_digest),
          objectKey: String(p.api_key),
          sizeBytes: Number(p.api_size),
          manifest: p.api_manifest as never,
        },
        {
          digest: String(p.runtime_digest),
          objectKey: String(p.runtime_key),
          sizeBytes: Number(p.runtime_size),
          manifest: p.runtime_manifest as never,
          mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip",
        },
        // Failed releases retry after a backoff, at most five times per package;
        // an owner's cancellation is final. Each retry needs a new, replayable key.
        `fleet-install:${i.id}:${i.package_id}${i.release_id ? `:${i.release_id}` : ""}`,
        undefined,
        p.prepared_manifest_key ? String(i.package_id) : undefined,
      );
      await this.db.query(
        `UPDATE cantelop_control.fleet_installations SET release_id=$3,error_code=NULL,lease_until=NULL,updated_at=now()
        WHERE id=$1 AND package_id=$2 AND desired_state='ready'`,
        [i.id, i.package_id, release.id],
      );
    } catch {
      // No provider diagnostics/credentials in user-facing errors. Keep the intent retryable.
      await this.db.query(
        `UPDATE cantelop_control.fleet_installations SET error_code='installation_retry_pending',updated_at=now() WHERE id=$1`,
        [i.id],
      );
    }
    return true;
  }
}
