import type { SQLPool } from "../db/database";
import type { OperationRepository } from "../db/operation-repository";
import type { ReleaseRepository } from "../db/release-repository";
import type { PreparedArtifactStore } from "../session-runtime-materializations/prepared-artifact-store";
import { SessionRuntimeProviderError, type SessionRuntimeProvider } from "../session-runtime-materializations/provider";

/** App release receipts refer to package assets; no deployment or conversion. */
export class FleetAttachmentWorker {
  constructor(private readonly workerID: string, private readonly db: SQLPool,
    private readonly operations: OperationRepository, private readonly releases: ReleaseRepository,
    private readonly artifacts: Pick<PreparedArtifactStore, "readManifest" | "signImageDownload">,
    private readonly runtime: SessionRuntimeProvider) {}

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "attach_package");
    if (!operation) return false;
    const input = operation.input;
    if (typeof input.package_id !== "string" || !/^pkg_[0-9a-f]{32}$/.test(input.package_id) ||
        !["api", "session_runtime"].includes(String(input.component))) {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    await this.releases.withDeploymentLock(operation.appID, async () => {
      const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
      if (current.status !== "running" || current.lockedBy !== this.workerID) return;
      await this.releases.startComponent(operation.id, this.workerID);
      try {
        const result = await this.db.query(`SELECT p.prepared_manifest_key,p.prepared_manifest_digest
FROM cantelop_control.fleet_installations i
JOIN cantelop_control.fleets f ON f.id=i.fleet_id
JOIN cantelop_control.fleet_packages p ON p.id=i.package_id AND p.fleet_id=i.fleet_id
JOIN cantelop_control.releases r ON r.app_id=i.app_id AND r.id=$4
WHERE i.app_id=$1 AND i.package_id=$2 AND f.owner_user_id=$3 AND i.desired_state='ready'
  AND p.disabled_at IS NULL AND p.edge_ready_at IS NOT NULL AND p.prepared_manifest_key IS NOT NULL
  AND r.api_artifact_id=p.api_artifact_id AND r.session_runtime_artifact_id=p.runtime_artifact_id`,
        [operation.appID, input.package_id, operation.ownerUserID, input.release_id]);
        const pkg = result.rows[0];
        if (!pkg) {
          await this.releases.failComponent(operation.id, this.workerID, "package_binding_invalid");
          return;
        }
        if (input.component === "api") {
          await this.releases.succeedComponent(operation.id, this.workerID, { script_name: input.package_id, package_id: input.package_id });
          return;
        }
        const manifest = await this.artifacts.readManifest(String(pkg.prepared_manifest_key), String(pkg.prepared_manifest_digest));
        const imageURL = await this.artifacts.signImageDownload(manifest.image_key, 3600);
        const attached = await this.runtime.materialize({ commandID: operation.id, appID: operation.appID,
          ownerUserID: operation.ownerUserID, imageURL, manifest });
        if (attached.status === "failed") {
          await this.releases.failComponent(operation.id, this.workerID, attached.errorCode ?? "package_attachment_failed");
        } else if (attached.status === "succeeded") {
          if (attached.buildID !== manifest.build_id) throw new Error("attached Build mismatch");
          await this.releases.succeedComponent(operation.id, this.workerID, {
            package_id: input.package_id, provider_operation_id: attached.operationID,
            build_id: attached.buildID, build_version: attached.buildVersion,
            prepared_manifest_key: pkg.prepared_manifest_key, prepared_manifest_digest: pkg.prepared_manifest_digest,
          });
        } else {
          await this.releases.retryComponent(operation.id, this.workerID, new Date(Date.now() + 30_000));
        }
      } catch (error) {
        if (operation.attempts >= 100 || error instanceof SessionRuntimeProviderError && !error.retryable) {
          await this.runtime.cancel?.(operation.ownerUserID, operation.appID, operation.id).catch(() => {});
          await this.releases.failComponent(operation.id, this.workerID,
            error instanceof SessionRuntimeProviderError && error.errorCode ? error.errorCode : "package_attachment_failed");
        } else {
          await this.releases.retryComponent(operation.id, this.workerID, new Date(Date.now() + 30_000));
        }
      }
    });
    return true;
  }
}
