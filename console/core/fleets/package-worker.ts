import { createHash } from "node:crypto";
import { withTransaction, type SQLPool } from "../db/database";
import type { APIArtifactStore } from "../api-deployments/artifact-store";
import type { EdgeGateProvider } from "../release-activation/edge-gate-provider";

/** Publish one immutable bundle per package, independent of installation count. */
export class FleetPackageWorker {
  constructor(private readonly db: SQLPool, private readonly artifacts: Pick<APIArtifactStore, "get">,
    private readonly edge: Pick<EdgeGateProvider, "publishPackage">) {}

  runOnce(): Promise<boolean> {
    return withTransaction(this.db, async tx => {
      const result = await tx.query(`SELECT p.id,p.edge_compatibility_date,a.object_key,a.digest FROM cantelop_control.fleet_packages p
JOIN cantelop_control.api_artifacts a ON a.id=p.api_artifact_id
WHERE p.prepared_manifest_key IS NOT NULL AND p.edge_ready_at IS NULL AND p.disabled_at IS NULL AND p.edge_retry_at<=now()
ORDER BY p.created_at FOR UPDATE OF p SKIP LOCKED LIMIT 1`);
      const pkg = result.rows[0];
      if (!pkg) return false;
      try {
        const module = await this.artifacts.get(String(pkg.object_key));
        if (`sha256:${createHash("sha256").update(module).digest("hex")}` !== pkg.digest) throw new Error("package integrity failure");
        await this.edge.publishPackage(String(pkg.id), { digest: String(pkg.digest),
          module: new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(module), compatibility_date: String(pkg.edge_compatibility_date) });
        await tx.query(`UPDATE cantelop_control.fleet_packages SET edge_ready_at=now() WHERE id=$1`, [pkg.id]);
      } catch {
        await tx.query(`UPDATE cantelop_control.fleet_packages SET edge_retry_at=now()+interval '30 seconds' WHERE id=$1`, [pkg.id]);
      }
      return true;
    });
  }
}
