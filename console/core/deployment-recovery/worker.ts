import type { OperationRepository } from "../db/operation-repository";
import type { ReleaseRepository } from "../db/release-repository";
import type { FireFuseSessionRuntimeProvider } from "../session-runtime-materializations/fire-fuse-provider";
import { assertResourceID } from "../domain/ids";
import type { RecoveryPrerequisiteRepository } from "../db/recovery-prerequisite-repository";

export class DeploymentRecoveryWorker {
  private lastRecoveryAt = 0;
  constructor(
    private readonly workerID: string,
    private readonly operations: OperationRepository,
    private readonly releases: ReleaseRepository,
    private readonly provider: Pick<FireFuseSessionRuntimeProvider, "cancel">,
    private readonly prerequisites: Pick<RecoveryPrerequisiteRepository, "prepare">,
    private readonly now: () => Date = () => new Date(),
  ) {}

  async runOnce(): Promise<boolean> {
    const at = this.now().getTime();
    if (at - this.lastRecoveryAt >= 60_000) {
      // A quick process restart can happen before its previous claim expires.
      // Sweep periodically so recovery does not need another manual restart.
      await this.operations.recoverStaleCancellations();
      this.lastRecoveryAt = at;
    }
    const operation = await this.operations.claim(this.workerID, "cancel_release");
    if (!operation) return false;
    const releaseID = operation.input.release_id;
    if (typeof releaseID !== "string" || typeof operation.input.restart !== "boolean") throw new TypeError("invalid recovery input");
    assertResourceID("release", releaseID);
    try {
      await this.releases.withDeploymentLock(operation.appID, async () => {
        const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
        if (current.status !== "running" || current.lockedBy !== this.workerID) return;
        const readiness = await this.prerequisites.prepare(operation.id, this.workerID);
        if (readiness !== "ready") {
          await this.operations.recordProgress(operation.id, this.workerID, `recovery_${readiness}`);
          await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + 30_000));
          return;
        }
        const release = await this.releases.getForUser(operation.ownerUserID, operation.appID, releaseID);
        await this.operations.recordProgress(operation.id, this.workerID, "provider_cancelling");
        await this.provider.cancel(operation.ownerUserID, operation.appID, release.components.session_runtime.operationID);
        await this.releases.completeRecovery(operation.id, this.workerID);
      });
    } catch {
      // Keep the App blocked until provider cancellation is acknowledged. Never
      // let an outage silently turn a cancellation into an unsafe replacement.
      const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
      if (current.status === "running" && current.lockedBy === this.workerID) {
        await this.operations.recordProgress(operation.id, this.workerID, "recovery_acknowledgement_pending");
        await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + 30_000));
      }
    }
    return true;
  }
}
