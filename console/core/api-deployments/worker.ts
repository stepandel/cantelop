import { createHash } from "node:crypto";

import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import type { ReleaseRepository } from "../db/release-repository";
import type { ConfigRepository } from "../configuration/repository";
import type { APIConfigurationProvider } from "../configuration/provider";
import { assertResourceID } from "../domain/ids";
import type { APIArtifactStore } from "./artifact-store";
import { APIProviderError, type APIProvider } from "./provider";

const maxAttempts = 10;

export class APIDeploymentWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: OperationRepository,
    private readonly releases: Pick<ReleaseRepository,
      "withDeploymentLock" | "startComponent" | "succeedComponent" | "failComponent" | "retryComponent">,
    private readonly artifacts: APIArtifactStore,
    private readonly provider: APIProvider,
    private readonly configuration: Pick<ConfigRepository, "projection">,
    private readonly configurationProvider: APIConfigurationProvider,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid API deployment worker ID");
  }

  recoverStale(): Promise<number> {
    return this.operations.recoverStale(5 * 60 * 1000);
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "deploy_api");
    if (!operation) return false;
    const execute = async () => {
      const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
      if (current.status !== "running" || current.lockedBy !== this.workerID) return true;
      try { return await this.runClaimed(operation); }
      catch (error) {
        const latest = await this.operations.getForUser(operation.ownerUserID, operation.id);
        if (latest.status !== "running" || latest.lockedBy !== this.workerID) return true;
        throw error;
      }
    };
    return this.releases.withDeploymentLock(operation.appID, execute);
  }

  private async runClaimed(operation: ControlOperation): Promise<boolean> {
    let input: DeploymentOperationInput;
    try {
      input = deploymentInput(operation);
    } catch {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    await this.releases.startComponent(operation.id, this.workerID);
    await this.operations.recordProgress(operation.id, this.workerID, "artifact_verifying");
    let module: Buffer;
    try {
      module = await this.artifacts.get(input.objectKey);
      const digest = `sha256:${createHash("sha256").update(module).digest("hex")}`;
      if (digest !== input.artifactDigest) throw new Error("artifact digest mismatch");
      new TextDecoder("utf-8", { fatal: true }).decode(module);
    } catch {
      await this.fail(operation, "artifact_integrity_failed");
      return true;
    }
    try {
      await this.operations.recordProgress(operation.id, this.workerID, "api_deploying");
      const release = await this.provider.deploy(operation.appID, input.releaseID, module);
      if (release.scriptName !== input.releaseID) throw new Error("provider script identity mismatch");
      const projection = await this.configuration.projection(operation.appID);
      // Revision 0 means the App was never configured: there is nothing to bind.
      if (projection.revision > 0) {
        await this.configurationProvider.replaceConfiguration({ appID: operation.appID,
          scriptName: input.releaseID, ...projection });
      }
      await this.releases.succeedComponent(operation.id, this.workerID, {
        provider_release_id: release.id,
        provider_etag: release.etag,
        script_name: release.scriptName,
      });
    } catch (error) {
      await this.operations.recordProgress(operation.id, this.workerID, error instanceof APIProviderError ? error.progress : "api_provider_failed");
      if (operation.attempts >= maxAttempts) {
        await this.fail(operation, "provider_deployment_failed");
      } else {
        const delaySeconds = Math.min(300, 2 ** Math.min(operation.attempts, 8));
        const availableAt = new Date(this.now().getTime() + delaySeconds * 1000);
        await this.releases.retryComponent(operation.id, this.workerID, availableAt);
      }
    }
    return true;
  }

  private async fail(operation: ControlOperation, errorCode: string): Promise<void> {
    await this.releases.failComponent(operation.id, this.workerID, errorCode);
  }
}

interface DeploymentOperationInput {
  releaseID: string;
  artifactID: string;
  objectKey: string;
  artifactDigest: string;
}

function deploymentInput(operation: ControlOperation): DeploymentOperationInput {
  const input = operation.input;
  const keys = Object.keys(input).sort();
  if (keys.join(",") !== "artifact_digest,artifact_id,object_key,release_id" ||
      typeof input.artifact_id !== "string" || typeof input.release_id !== "string" ||
      typeof input.object_key !== "string" || typeof input.artifact_digest !== "string") {
    throw new TypeError("invalid deployment operation input");
  }
  assertResourceID("artifact", input.artifact_id);
  assertResourceID("release", input.release_id);
  if (!/^deployment-uploads\/[0-9a-f]{32}\/api\/worker\.mjs$/.test(input.object_key) ||
      !/^sha256:[0-9a-f]{64}$/.test(input.artifact_digest)) {
    throw new TypeError("invalid deployment operation input");
  }
  return {
    releaseID: input.release_id,
    artifactID: input.artifact_id,
    objectKey: input.object_key,
    artifactDigest: input.artifact_digest,
  };
}
