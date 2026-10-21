import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import { appSlugPattern } from "../domain/model";
import { AppRoutingProviderError, type AppRoutingProvider } from "../app-routing/provider";
import { ComputeRegistrationProviderError, type ComputeRegistrationProvider } from "./provider";

const maxAttempts = 20;
const retryDelayMilliseconds = 30_000;

export class ComputeRegistrationWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: Pick<OperationRepository, "claim" | "succeed" | "fail" | "retry">,
    private readonly provider: ComputeRegistrationProvider,
    private readonly routing: Pick<AppRoutingProvider, "publish">,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid compute registration worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "register_app");
    if (!operation) return false;
    let input: RegistrationInput;
    try {
      input = registrationInput(operation);
    } catch {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    try {
      await this.provider.registerApp({
        ownerUserID: operation.ownerUserID,
        appID: operation.appID,
        appSlug: input.appSlug,
        internalPort: input.internalPort,
 ...(input.managed?{managed:true}:{}),
      });
      await this.routing.publish({ appID: operation.appID, appSlug: input.appSlug });
      await this.operations.succeed(operation.id, this.workerID, { provider_app_id: operation.appID });
    } catch (error) {
      if ((error instanceof ComputeRegistrationProviderError || error instanceof AppRoutingProviderError) && !error.retryable) {
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

interface RegistrationInput {
 managed?:boolean;
  appSlug: string;
  internalPort: number;
}

function registrationInput(operation: ControlOperation): RegistrationInput {
  const input = operation.input;
  if (Object.keys(input).filter(k=>k!=="managed").sort().join(",") !== "app_slug,internal_port" || (input.managed!==undefined&&input.managed!==true) ||
      typeof input.app_slug !== "string" ||
      !Number.isInteger(input.internal_port)) throw new TypeError("invalid compute registration operation input");
  if (!appSlugPattern.test(input.app_slug) ||
      (input.internal_port as number) < 1 || (input.internal_port as number) > 65535) {
    throw new TypeError("invalid compute registration operation input");
  }
  return {
    appSlug: input.app_slug,
 ...(input.managed===true?{managed:true}:{}),
    internalPort: input.internal_port as number,
  };
}
