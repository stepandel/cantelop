import type { ComputeRegistrationProvider } from "../compute-registrations/provider";
import { ComputeRegistrationProviderError } from "../compute-registrations/provider";
import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import { ControlError } from "../domain/errors";
import type { ReleaseRepository } from "../db/release-repository";
import type { ConfigRepository } from "./repository";
import { ConfigurationProviderError, type APIConfigurationProvider } from "./provider";

const maxAttempts = 100;
const retryDelayMilliseconds = 30_000;

export class ConfigurationProjectionWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository, "claim" | "succeed" | "fail" | "retry" | "recordEdgeConfiguration">,
    private readonly configuration: Pick<ConfigRepository, "projection" | "revision">,
    private readonly apiProvider: APIConfigurationProvider,
    private readonly computeProvider: Pick<ComputeRegistrationProvider, "replaceConfiguration">,
    private readonly releases: Pick<ReleaseRepository, "withDeploymentLock" | "activeScript">,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid configuration worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "sync_config");
    if (!operation) return false;
    return this.releases.withDeploymentLock(operation.appID, () => this.runClaimed(operation));
  }

  private async runClaimed(operation: ControlOperation): Promise<boolean> {
    let wantedRevision: number;
    try { wantedRevision = operationRevision(operation); }
    catch {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    try {
      const projection = await this.configuration.projection(operation.appID);
      if (projection.revision !== wantedRevision) {
        await this.operations.fail(operation.id, this.workerID, "config_superseded");
        return true;
      }
      const scriptName = await this.releases.activeScript(operation.appID);
      await this.apiProvider.replaceConfiguration({ appID: operation.appID, scriptName, ...projection });
      await this.operations.recordEdgeConfiguration(operation.id, this.workerID, wantedRevision);
      await this.computeProvider.replaceConfiguration({
        ownerUserID: operation.ownerUserID,
        appID: operation.appID,
        revision: projection.revision,
        values: projection.values,
      });
      if (await this.configuration.revision(operation.appID) !== wantedRevision) {
        await this.operations.fail(operation.id, this.workerID, "config_superseded");
      } else {
        await this.operations.succeed(operation.id, this.workerID, { revision: wantedRevision });
      }
    } catch (error) {
      if (error instanceof ControlError && error.code === "release_unsupported") {
        // The live release predates SDK 1.0; no retry will give it a release-scoped script.
        await this.operations.fail(operation.id, this.workerID, "release_unsupported");
      } else if ((error instanceof ConfigurationProviderError || error instanceof ComputeRegistrationProviderError) && !error.retryable) {
        await this.operations.fail(operation.id, this.workerID, "provider_configuration_rejected");
      } else if (operation.attempts >= maxAttempts) {
        await this.operations.fail(operation.id, this.workerID, "provider_configuration_timeout");
      } else {
        await this.operations.retry(operation.id, this.workerID, new Date(this.now().getTime() + retryDelayMilliseconds));
      }
    }
    return true;
  }
}

function operationRevision(operation: ControlOperation): number {
  if (Object.keys(operation.input).join(",") !== "revision" || !Number.isSafeInteger(operation.input.revision) ||
      (operation.input.revision as number) < 1) throw new TypeError("invalid configuration operation input");
  return operation.input.revision as number;
}
