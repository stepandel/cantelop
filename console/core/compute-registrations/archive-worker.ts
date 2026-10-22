import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import { appSlugPattern } from "../domain/model";
import type { AppRoutingProvider } from "../app-routing/provider";
import type { APIProvider } from "../api-deployments/provider";
import type { ProductRepository } from "../db/product-repository";
import type { ReleaseRepository } from "../db/release-repository";
import type { EdgeGateProvider } from "../release-activation/edge-gate-provider";
import { SessionRuntimeProviderError, type SessionRuntimeProvider } from "../session-runtime-materializations/provider";
import type { ComputeRegistrationProvider } from "./provider";

const retryDelayMilliseconds = 30_000;
const blockingOperationKinds = [
  "register_app",
  "register_workspace",
  "archive_workspace",
  "purge_workspace",
  "deploy_api",
  "materialize_build",
  "attach_package",
  "sync_config",
] as const;

export class ComputeAppArchiveWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository, "claim" | "hasUnfinished" | "succeed" | "fail" | "retry">,
    private readonly provider: Pick<ComputeRegistrationProvider, "archiveApp">,
    private readonly routing: Pick<AppRoutingProvider, "remove">,
    private readonly products: Pick<ProductRepository, "nextArchiveGeneration" | "releaseScriptIDs" | "completeArchive">,
    private readonly releases: Pick<ReleaseRepository, "withDeploymentLock">,
    private readonly edge: Pick<EdgeGateProvider, "close" | "read"> & Partial<Pick<EdgeGateProvider, "purge">>,
    private readonly runtime: Pick<SessionRuntimeProvider, "setReleaseAdmission" | "releaseAdmission">,
    private readonly api: Pick<APIProvider, "removeRelease">,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid compute archive worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "archive_app");
    if (!operation) return false;
    if (Object.keys(operation.input).join(",") !== "app_slug" || typeof operation.input.app_slug !== "string" ||
        !appSlugPattern.test(operation.input.app_slug)) {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    const appSlug = operation.input.app_slug;
    await this.releases.withDeploymentLock(operation.appID, async () => {
      try {
        // Every provider mutation already in flight must become terminal before
        // delete; otherwise a slow operation could recreate or mutate provider
        // state after archival.
        for (const kind of blockingOperationKinds) {
          if (await this.operations.hasUnfinished(operation.ownerUserID, operation.appID, kind)) {
            await this.retry(operation);
            return;
          }
        }
        const generation = await this.products.nextArchiveGeneration();
        const edgeState = await this.edge.close(operation.appID, generation);
        if (edgeState.generation !== generation || !edgeState.closed) throw new Error("archive edge gate did not close");
        const edgeReadback = await this.edge.read(operation.appID);
        if (edgeReadback.generation !== generation || !edgeReadback.closed) throw new Error("archive edge readback mismatch");
        try {
          await this.runtime.setReleaseAdmission(operation.ownerUserID, operation.appID, generation, true);
          const host = await this.runtime.releaseAdmission(operation.ownerUserID, operation.appID);
          if (host.generation !== generation || !host.closed) throw new Error("archive host readback mismatch");
        } catch (error) {
          // An App that never reached compute registration has no Sessions to
          // admit. Registration creates its gate closed by default.
          if (!(error instanceof SessionRuntimeProviderError && error.progress === "provider_http_404")) throw error;
        }
        // Terminate existing Sandboxes before any Cloudflare cleanup that may
        // need a long retry; release admission only blocks new Sessions.
        await this.provider.archiveApp({ ownerUserID: operation.ownerUserID, appID: operation.appID });
        // Retain the closed identity tombstone, remove installation secrets and
        // permanently prevent delayed activation/configuration from reopening it.
        await this.edge.purge?.(operation.appID, generation);
        await this.products.completeArchive(operation.ownerUserID, operation.appID);
        await this.routing.remove({ appID: operation.appID, appSlug });
        for (const scriptID of await this.products.releaseScriptIDs(operation.appID)) {
          await this.api.removeRelease(operation.appID, scriptID);
        }
        await this.operations.succeed(operation.id, this.workerID, { provider_app_id: operation.appID });
      } catch {
        // Once either gate has closed, an uncertain provider result must keep
        // retrying until cleanup converges. An archived App never reopens.
        await this.retry(operation);
      }
    });
    return true;
  }

  private async retry(operation: ControlOperation): Promise<void> {
    await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + retryDelayMilliseconds));
  }
}
