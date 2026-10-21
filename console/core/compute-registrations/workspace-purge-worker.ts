import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import type { ProductRepository } from "../db/product-repository";
import { assertResourceID } from "../domain/ids";
import { ComputeRegistrationProviderError, type ComputeRegistrationProvider } from "./provider";

const maxAttempts = 100;
const retryDelayMilliseconds = 30_000;

export class ComputeWorkspacePurgeWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository,
      "claim" | "cancelQueuedWorkspaceRegistration" | "hasRunningWorkspaceRegistration" | "succeed" | "fail" | "retry">,
    private readonly provider: Pick<ComputeRegistrationProvider, "archiveWorkspace" | "purgeWorkspace">,
    private readonly workspaces: Pick<ProductRepository, "markWorkspaceHostArchived">,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid Workspace purge worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "purge_workspace");
    if (!operation) return false;
    const workspaceID = workspaceInput(operation);
    if (!workspaceID) {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    const command = { ownerUserID: operation.ownerUserID, appID: operation.appID, workspaceID };
    // The gateway opens registered Workspaces by slug without asking product
    // control, so hide this one on the host before any wait below. A
    // registration still in flight may restore it, so this early attempt must
    // not acknowledge deletion. Final archival below fences registration.
    await this.provider.archiveWorkspace(command).catch(() => undefined);
    try {
      // Do not let an in-flight registration recreate data after the purge.
      await this.operations.cancelQueuedWorkspaceRegistration(operation.ownerUserID, operation.appID, workspaceID);
      if (await this.operations.hasRunningWorkspaceRegistration(operation.ownerUserID, operation.appID, workspaceID)) {
        await this.retryOrFail(operation);
        return true;
      }
      // Registration may have completed between the early archive and the
      // running check. Archive again after it settles before acknowledging.
      let acknowledged = false;
      await this.provider.archiveWorkspace(command)
        .then(() => this.workspaces.markWorkspaceHostArchived(workspaceID))
        .then(() => { acknowledged = true; })
        .catch(() => undefined);
      // Purge itself guarantees absence even if the archive or marker failed.
      // A failed final marker write must retry rather than leave a succeeded
      // operation whose deletion acknowledgement remains pending forever.
      await this.provider.purgeWorkspace(command);
      if (!acknowledged) await this.workspaces.markWorkspaceHostArchived(workspaceID);
      await this.operations.succeed(operation.id, this.workerID, { provider_workspace_id: workspaceID });
    } catch (error) {
      if (error instanceof ComputeRegistrationProviderError && !error.retryable) {
        await this.operations.fail(operation.id, this.workerID, "provider_purge_rejected");
      } else {
        await this.retryOrFail(operation);
      }
    }
    return true;
  }

  private async retryOrFail(operation: ControlOperation): Promise<void> {
    if (operation.attempts >= maxAttempts) {
      await this.operations.fail(operation.id, this.workerID, "provider_purge_timeout");
    } else {
      await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + retryDelayMilliseconds));
    }
  }
}

function workspaceInput(operation: ControlOperation): string | undefined {
  if (Object.keys(operation.input).join(",") !== "workspace_id" || typeof operation.input.workspace_id !== "string") {
    return undefined;
  }
  try {
    assertResourceID("workspace", operation.input.workspace_id);
    return operation.input.workspace_id;
  } catch {
    return undefined;
  }
}
