import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import type { ComputeRegistrationRepository } from "../db/compute-registration-repository";
import { assertResourceID } from "../domain/ids";
import { appSlugPattern } from "../domain/model";
import { ComputeRegistrationProviderError, type ComputeRegistrationProvider } from "./provider";

const maxAttempts = 100;
const retryDelayMilliseconds = 30_000;

export class ComputeWorkspaceRegistrationWorker {
  // Registration is on the first-request path; keep idle pickup below activation latency.
  readonly idleDelayMilliseconds = 100;
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository, "claim" | "fail" | "retry">,
    private readonly registrations: Pick<ComputeRegistrationRepository, "succeedWorkspace" | "workspaceRegistrationContext">,
    private readonly provider: Pick<ComputeRegistrationProvider, "registerWorkspace">,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid Workspace registration worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "register_workspace");
    if (!operation) return false;
    let input: WorkspaceRegistrationInput;
    try {
      input = workspaceInput(operation);
    } catch {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    // Checked after the claim: an archive that commits later sees this
    // registration running and waits for it to settle before archiving.
    const registration=await this.registrations.workspaceRegistrationContext(operation.appID,input.workspaceID);
    if (!registration || registration.archived) {
      await this.operations.fail(operation.id, this.workerID, "workspace_archived");
      return true;
    }
    try {
      await this.provider.registerWorkspace({
        ownerUserID: operation.ownerUserID,
        appID: operation.appID,
        workspaceID: input.workspaceID,
        workspaceSlug: input.workspaceSlug,
        registration: {computeTenantID:registration.computeTenantID},
      });
      await this.registrations.succeedWorkspace(operation.id, this.workerID, input.workspaceID);
    } catch (error) {
      if (error instanceof ComputeRegistrationProviderError && !error.retryable) {
        await this.operations.fail(operation.id, this.workerID, "provider_registration_rejected");
      } else if (operation.attempts >= maxAttempts) {
        await this.operations.fail(operation.id, this.workerID, "provider_registration_timeout");
      } else {
        await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + retryDelayMilliseconds));
      }
    }
    return true;
  }
}

interface WorkspaceRegistrationInput {
  workspaceID: string;
  workspaceSlug: string;
}

function workspaceInput(operation: ControlOperation): WorkspaceRegistrationInput {
  const input = operation.input;
  if (Object.keys(input).sort().join(",") !== "workspace_id,workspace_slug" ||
      typeof input.workspace_id !== "string" || typeof input.workspace_slug !== "string") {
    throw new TypeError("invalid Workspace registration input");
  }
  assertResourceID("workspace", input.workspace_id);
  if (!appSlugPattern.test(input.workspace_slug)) throw new TypeError("invalid Workspace registration input");
  return { workspaceID: input.workspace_id, workspaceSlug: input.workspace_slug };
}
