import type { DeploymentArtifactStore } from "../deployment-uploads/artifact-store";
import type { ControlOperation, OperationRepository } from "../db/operation-repository";
import type { ReleaseRepository } from "../db/release-repository";
import { assertResourceID } from "../domain/ids";
import { SessionRuntimeProviderError, type SessionRuntimeProvider } from "./provider";
import { InvalidImageError, type ImageBuilder } from "./local-builder";
import type { PreparedArtifactStore } from "./prepared-artifact-store";

const maxAttempts = 100;
const retryDelayMilliseconds = 30_000;

export class SessionRuntimeMaterializationWorker {
  constructor(
    private readonly workerID: string,
    private readonly operations: OperationRepository,
    private readonly releases: Pick<ReleaseRepository, "startComponent" | "succeedComponent" | "failComponent" | "retryComponent">,
    private readonly artifacts: Pick<DeploymentArtifactStore, "signSessionRuntimeDownload">,
    private readonly builder: ImageBuilder,
    private readonly preparedArtifacts: PreparedArtifactStore,
    private readonly provider: SessionRuntimeProvider,
    private readonly now: () => Date = () => new Date(),
  ) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$/.test(workerID)) throw new TypeError("invalid Session runtime worker ID");
  }

  async runOnce(): Promise<boolean> {
    const operation = await this.operations.claim(this.workerID, "materialize_build");
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
    return execute();
  }

  private async runClaimed(operation: ControlOperation): Promise<boolean> {
    let input: SessionRuntimeOperationInput;
    try {
      input = sessionRuntimeInput(operation);
    } catch {
      await this.operations.fail(operation.id, this.workerID, "invalid_operation_input");
      return true;
    }
    await this.releases.startComponent(operation.id, this.workerID);

    try {
      const sourceURL = await this.artifacts.signSessionRuntimeDownload(input.objectKey, 3600);
      const cancellation = new AbortController();
      let checking = false;
      const checkCancellation = setInterval(async () => {
        if (checking) return;
        checking = true;
        try {
          const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
          if (current.status !== "running" || current.lockedBy !== this.workerID) cancellation.abort();
        } catch { /* A transient database failure does not cancel a running conversion. */ }
        finally { checking = false; }
      }, 2_000);
      let prepared;
      try {
        prepared = await this.builder.prepare(operation.id, sourceURL, input.artifactDigest, input.sizeBytes, cancellation.signal);
      } finally {
        clearInterval(checkCancellation);
      }
      let materialization;
      let manifestRecord;
      try {
        const imageKey = await this.preparedArtifacts.uploadImage(
          prepared.imagePath, prepared.manifest.rootfs_sha256, prepared.manifest.rootfs_size_bytes,
        );
        if (imageKey !== prepared.manifest.image_key) throw new Error("prepared image key mismatch");
        manifestRecord = await this.preparedArtifacts.uploadManifest(prepared.manifest);
        const current = await this.operations.getForUser(operation.ownerUserID, operation.id);
        if (current.status !== "running" || current.lockedBy !== this.workerID) return true;
        const imageURL = await this.preparedArtifacts.signImageDownload(imageKey, 3600);
        materialization = await this.provider.materialize({
          commandID: operation.id, ownerUserID: operation.ownerUserID, appID: operation.appID,
          imageURL, manifest: prepared.manifest,
        });
      } finally {
        await this.builder.removeImage(prepared.imagePath);
      }
      await this.operations.recordProgress(operation.id, this.workerID, `provider_${materialization.status}`);
      if (materialization.status === "succeeded") {
        await this.releases.succeedComponent(operation.id, this.workerID, {
          provider_operation_id: materialization.operationID,
          build_id: materialization.buildID,
          build_version: materialization.buildVersion,
          prepared_manifest_key: manifestRecord.key,
          prepared_manifest_digest: manifestRecord.digest,
        });
      } else if (materialization.status === "failed") {
        await this.releases.failComponent(operation.id, this.workerID, materialization.errorCode ?? "provider_materialization_failed");
      } else {
        await this.retryOrFail(operation);
      }
    } catch (error) {
      await this.operations.recordProgress(operation.id, this.workerID, error instanceof SessionRuntimeProviderError ? error.progress : "artifact_or_provider_unavailable");
      if (error instanceof InvalidImageError) {
        await this.releases.failComponent(operation.id, this.workerID, "invalid_session_image");
      } else if (error instanceof SessionRuntimeProviderError && !error.retryable) {
        await this.cancelProvider(operation);
        await this.releases.failComponent(operation.id, this.workerID, error.errorCode ?? "provider_materialization_rejected");
      } else {
        await this.retryOrFail(operation);
      }
    }
    return true;
  }

  private async retryOrFail(operation: ControlOperation): Promise<void> {
    if (operation.attempts >= maxAttempts) {
      await this.cancelProvider(operation);
      await this.releases.failComponent(operation.id, this.workerID, "provider_materialization_timeout");
      return;
    }
    await this.releases.retryComponent(
      operation.id,
      this.workerID,
      new Date(this.now().getTime() + retryDelayMilliseconds),
    );
  }

  // A provider Build left receiving blocks every later Build for the App, so
  // release it before giving up. Best effort: the host may never have begun it.
  private async cancelProvider(operation: ControlOperation): Promise<void> {
    await this.provider.cancel?.(operation.ownerUserID, operation.appID, operation.id).catch(() => {});
  }
}

interface SessionRuntimeOperationInput {
  releaseID: string;
  artifactID: string;
  objectKey: string;
  artifactDigest: string;
  sizeBytes: number;
}

function sessionRuntimeInput(operation: ControlOperation): SessionRuntimeOperationInput {
  const input = operation.input;
  if (Object.keys(input).sort().join(",") !== "artifact_digest,artifact_id,object_key,release_id,size_bytes" ||
      typeof input.release_id !== "string" || typeof input.artifact_id !== "string" ||
      typeof input.object_key !== "string" || typeof input.artifact_digest !== "string" ||
      !Number.isSafeInteger(input.size_bytes) || (input.size_bytes as number) < 1 || (input.size_bytes as number) > 20 * 1024 ** 3) {
    throw new TypeError("invalid Session runtime operation input");
  }
  assertResourceID("release", input.release_id);
  assertResourceID("sessionRuntimeArtifact", input.artifact_id);
  if (!/^deployment-uploads\/[0-9a-f]{32}\/session-runtime\/image\.tar\.gz$/.test(input.object_key) ||
      !/^sha256:[0-9a-f]{64}$/.test(input.artifact_digest)) throw new TypeError("invalid Session runtime operation input");
  return {
    releaseID: input.release_id,
    artifactID: input.artifact_id,
    objectKey: input.object_key,
    artifactDigest: input.artifact_digest,
    sizeBytes: input.size_bytes as number,
  };
}
