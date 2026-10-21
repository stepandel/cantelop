import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import { assertResourceID } from "../domain/ids";
import { ComputeRegistrationProviderError, type ComputeRegistrationProvider } from "./provider";

const maxAttempts = 100;
const retryDelayMilliseconds = 30_000;

export class ComputeWorkspaceArchiveWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository,
      "claim" | "cancelQueuedWorkspaceRegistration" | "hasRunningWorkspaceRegistration" | "succeed" | "fail" | "retry">,
    private readonly provider: Pick<ComputeRegistrationProvider, "archiveWorkspace">,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid Workspace archive worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "archive_workspace");
    if (!operation) return false;
    const workspaceID = workspaceInput(operation);
    if (!workspaceID) {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    try {
      // A registration must never recreate the Workspace after archival.
      // Registrations claimed from now on see archived_at and fail on their
      // own; one already in flight may still reach the provider, so wait for
      // it to settle. The next attempt either cancels its retry or archives
      // its result.
      await this.operations.cancelQueuedWorkspaceRegistration(operation.ownerUserID, operation.appID, workspaceID);
      if (await this.operations.hasRunningWorkspaceRegistration(operation.ownerUserID, operation.appID, workspaceID)) {
        await this.retryOrFail(operation);
        return true;
      }
      await this.provider.archiveWorkspace({
        ownerUserID: operation.ownerUserID,
        appID: operation.appID,
        workspaceID,
      });
      await this.operations.succeed(operation.id, this.workerID, { provider_workspace_id: workspaceID });
    } catch (error) {
      if (error instanceof ComputeRegistrationProviderError && !error.retryable) {
        await this.operations.fail(operation.id, this.workerID, "provider_archive_rejected");
      } else {
        await this.retryOrFail(operation);
      }
    }
    return true;
  }

  private async retryOrFail(operation: ControlOperation): Promise<void> {
    if (operation.attempts >= maxAttempts) {
      await this.operations.fail(operation.id, this.workerID, "provider_archive_timeout");
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
