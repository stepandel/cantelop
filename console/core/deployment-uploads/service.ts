import {parseSessionRuntimeManifest,validateArtifactPair} from "../api-deployments/protocol-artifact";
import { ControlError } from "../domain/errors";
import { newResourceID } from "../domain/ids";
import type {
  DeploymentUpload,
  DeploymentUploadInput,
  DeploymentUploadRepository,
} from "../db/deployment-upload-repository";
import { releaseBuildID } from "../db/release-build-id";
import type { ProductRelease, ReleaseRepository } from "../db/release-repository";
import { parseAPIArtifactManifest } from "../api-deployments/artifact";
import type { AppDefinitionSummary } from "../api-deployments/app-definition";
import type { CompletedUploadPart, DeploymentArtifactStore, MultipartUpload } from "./artifact-store";

const digestPattern = /^sha256:[0-9a-f]{64}$/;
const sessionRuntimeMediaType = "application/vnd.cantelop.session-runtime.image.v1.tar+gzip" as const;
const uploadLifetimeMilliseconds = 60 * 60_000;
const signedURLLifetimeSeconds = 15 * 60;

export interface PublicDeploymentUpload {
  id: string;
  app_id: string;
  status: "uploading" | "finalized";
  expires_at: string;
  release_id?: string;
  api?: {
    method: "PUT";
    url: string;
    headers: Readonly<Record<string, string>>;
  };
  session_runtime?: {
    part_size_bytes: number;
    parts: Array<{
      part_number: number;
      method: "PUT";
      url: string;
      headers: Readonly<Record<string, string>>;
    }>;
  };
}

export interface PublicRelease {
  id: string;
  app_id: string;
  version: number;
  status: ProductRelease["status"];
  api_artifact_digest: string;
  session_runtime_artifact_digest: string;
  components: {
    api: PublicReleaseComponent;
    session_runtime: PublicReleaseComponent;
  };
  error_code?: string;
  created_at: string;
  updated_at: string;
  activated_at?: string;
  /** What the App definition declared; absent only for releases that predate SDK 1.0. */
  definition?: AppDefinitionSummary;
}

interface PublicReleaseComponent {
  attempts?: number;
  progress?: string;
  updated_at?: string;
  next_attempt_at?: string;
  status: ProductRelease["components"]["api"]["status"];
  error_code?: string;
}

export class DeploymentService {
  constructor(
    private readonly uploads: DeploymentUploadRepository,
    private readonly releases: ReleaseRepository,
    private readonly artifacts: DeploymentArtifactStore,
    private readonly now: () => Date = () => new Date(),
    private readonly resolveOwner: (userID: string, appID: string) => Promise<string> = async (userID) => userID,
    private readonly prepareIntegration?: (userID:string,appID:string,manifest:import("../api-deployments/protocol-artifact").ProtocolEdgeManifest)=>Promise<void>,
    /** Build IDs the host still retains for an App, or null when unknown. */
    private readonly retainedBuilds?: (appID: string) => Promise<Set<string> | null>,
  ) {}

  async beginUpload(
    userID: string,
    appID: string,
    rawInput: unknown,
    idempotencyKey: string,
  ): Promise<PublicDeploymentUpload> {
    const ownerUserID = await this.resolveOwner(userID, appID);
    const manifest = validateBundleManifest(rawInput);
 await this.prepareIntegration?.(userID,appID,manifest.api.manifest);
    const candidateID = newResourceID("deploymentUpload");
    const suffix = candidateID.slice("upl_".length);
    const input: DeploymentUploadInput = {
      api: {
        ...manifest.api,
        objectKey: `deployment-uploads/${suffix}/api/worker.mjs`,
      },
      sessionRuntime: {
        ...manifest.sessionRuntime,
        objectKey: `deployment-uploads/${suffix}/session-runtime/image.tar.gz`,
      },
    };
    let upload = await this.uploads.begin(
      candidateID,
      ownerUserID,
      appID,
      input,
      idempotencyKey,
      new Date(this.now().getTime() + uploadLifetimeMilliseconds),
    );
    if (upload.status === "finalized") return publicFinalizedUpload(upload);
    if (upload.status === "expired" || upload.status === "cancelled") throw new ControlError("conflict");
    if (upload.status === "preparing") upload = await this.prepareUpload(upload);
    if (upload.status !== "uploading" || !upload.storeUploadID ||
        !upload.sessionRuntimePartSizeBytes || !upload.sessionRuntimePartCount) throw new ControlError("conflict");
    return this.signUpload(upload);
  }

  async finalizeUpload(
    userID: string,
    appID: string,
    uploadID: string,
    rawInput: unknown,
  ): Promise<PublicRelease> {
    const ownerUserID = await this.resolveOwner(userID, appID);
    const parts = validateCompletedParts(rawInput);
    const upload = await this.uploads.getForUser(ownerUserID, appID, uploadID);
    if (upload.status === "finalized" && upload.releaseID) {
      return publicRelease(await this.releases.getForUser(ownerUserID, appID, upload.releaseID));
    }
    if (upload.status !== "uploading" || upload.expiresAt <= this.now() || !upload.storeUploadID ||
        !upload.sessionRuntimePartSizeBytes || parts.length !== upload.sessionRuntimePartCount) throw new ControlError("conflict");

    const apiState = await this.artifacts.inspectObject(
      upload.input.api.objectKey,
      upload.input.api.sizeBytes,
      upload.input.api.digest,
    );
    if (apiState !== "matches") throw new ControlError("conflict");
    const sessionRuntimeState = await this.artifacts.inspectObject(
      upload.input.sessionRuntime.objectKey,
      upload.input.sessionRuntime.sizeBytes,
      upload.input.sessionRuntime.digest,
    );
    if (sessionRuntimeState === "mismatch") {
      await this.artifacts.deleteSessionRuntimeObject(upload.input.sessionRuntime.objectKey);
      throw new ControlError("conflict");
    }
    if (sessionRuntimeState === "missing") {
      await this.artifacts.completeSessionRuntimeUpload(
        upload.input.sessionRuntime.objectKey,
        upload.storeUploadID,
        parts,
        upload.input.sessionRuntime.sizeBytes,
        upload.sessionRuntimePartSizeBytes,
      );
      if (await this.artifacts.inspectObject(
        upload.input.sessionRuntime.objectKey,
        upload.input.sessionRuntime.sizeBytes,
        upload.input.sessionRuntime.digest,
      ) !== "matches") {
        await this.artifacts.deleteSessionRuntimeObject(upload.input.sessionRuntime.objectKey);
        throw new ControlError("conflict");
      }
    }

    const release = await this.releases.begin(
      ownerUserID,
      appID,
      upload.input.api,
      upload.input.sessionRuntime,
      upload.idempotencyKey,
      upload.id,
    );
    return publicRelease(release);
  }

  async listUploads(userID: string, appID: string) {
    const ownerUserID = await this.resolveOwner(userID, appID);
    return { items: (await this.uploads.listPendingForUser(ownerUserID, appID)).map(publicUploadSummary) };
  }

  async cancelUpload(userID: string, appID: string, uploadID: string) {
    const ownerUserID = await this.resolveOwner(userID, appID);
    const upload = await this.uploads.cancelForUser(ownerUserID, appID, uploadID);
    // Commit cancellation first: storage outages must not keep the App locked.
    // Repeating cancellation retries cleanup without touching a newer upload.
    let cleanupPending = false;
    if (upload.storeUploadID) {
      const results = await Promise.allSettled([
        this.artifacts.abortSessionRuntimeUpload(upload.input.sessionRuntime.objectKey, upload.storeUploadID),
        this.artifacts.deleteSessionRuntimeObject(upload.input.sessionRuntime.objectKey),
      ]);
      cleanupPending = results.some((result) => result.status === "rejected");
    }
    return { ...publicUploadSummary(upload), cleanup_pending: cleanupPending, idempotency_key: upload.idempotencyKey };
  }

  async get(userID: string, appID: string, releaseID: string): Promise<PublicRelease> {
    return publicRelease(await this.releases.getForUser(await this.resolveOwner(userID, appID), appID, releaseID));
  }

  async list(userID: string, appID: string): Promise<{ items: PublicRelease[] }> {
    return { items: (await this.releases.listForUser(await this.resolveOwner(userID, appID), appID)).map(publicRelease) };
  }

  /** Each operable App's live release version, keyed by App ID; Apps with none are absent. */
  async activeReleases(userID: string): Promise<Record<string, { version: number; activated_at?: string }>> {
    const active = await this.releases.activeReleasesForUser(userID);
    return Object.fromEntries(active.map(({ appID, version, activatedAt }) => [appID, {
      version,
      ...(activatedAt ? { activated_at: activatedAt.toISOString() } : {}),
    }]));
  }

  async recover(userID: string, appID: string, releaseID: string, restart: boolean): Promise<PublicRelease> {
    return publicRelease(await this.releases.requestRecovery(await this.resolveOwner(userID, appID), appID, releaseID, restart));
  }

  async rollback(userID: string, appID: string, expectedTargetID?: string): Promise<PublicRelease> {
    const ownerUserID = await this.resolveOwner(userID, appID);
    // Retention and image collection drop older Builds from the host. Reject a
    // rollback to one of them before activation closes release admission.
    if (this.retainedBuilds) {
      const target = await this.releases.rollbackTargetForUser(ownerUserID, appID);
      const buildID = target && (expectedTargetID === undefined || target.id === expectedTargetID) ? releaseBuildID(target) : null;
      if (target && buildID) {
        const retained = await this.retainedBuilds(appID);
        if (retained && !retained.has(buildID)) throw new ControlError("target_build_missing");
        // Activate exactly the release that was checked.
        expectedTargetID = target.id;
      }
    }
    return publicRelease(await this.releases.rollbackForUser(ownerUserID, appID, expectedTargetID));
  }

  private async prepareUpload(upload: DeploymentUpload): Promise<DeploymentUpload> {
    const multipart = await this.artifacts.createSessionRuntimeUpload(
      upload.input.sessionRuntime.objectKey,
      upload.input.sessionRuntime.sizeBytes,
      upload.input.sessionRuntime.digest,
    );
    try {
      return await this.uploads.markReady(
        upload.ownerUserID,
        upload.appID,
        upload.id,
        multipart.storeUploadID,
        multipart.partSizeBytes,
        multipart.partCount,
      );
    } catch (error) {
      await this.artifacts.abortSessionRuntimeUpload(upload.input.sessionRuntime.objectKey, multipart.storeUploadID).catch(() => undefined);
      if (!(error instanceof ControlError) || error.code !== "conflict") throw error;
      const winner = await this.uploads.getForUser(upload.ownerUserID, upload.appID, upload.id);
      if (winner.status !== "uploading") throw error;
      return winner;
    }
  }

  private async signUpload(upload: DeploymentUpload): Promise<PublicDeploymentUpload> {
    const multipart: MultipartUpload = {
      storeUploadID: upload.storeUploadID!,
      partSizeBytes: upload.sessionRuntimePartSizeBytes!,
      partCount: upload.sessionRuntimePartCount!,
    };
    const [api, sessionRuntimeParts] = await Promise.all([
      this.artifacts.signAPIUpload(
        upload.input.api.objectKey,
        upload.input.api.sizeBytes,
        upload.input.api.digest,
        signedURLLifetimeSeconds,
      ),
      this.artifacts.signSessionRuntimeParts(
        upload.input.sessionRuntime.objectKey,
        multipart,
        upload.input.sessionRuntime.sizeBytes,
        signedURLLifetimeSeconds,
      ),
    ]);
    return {
      id: upload.id,
      app_id: upload.appID,
      status: "uploading",
      expires_at: upload.expiresAt.toISOString(),
      api: { method: "PUT", url: api.url, headers: api.headers },
      session_runtime: {
        part_size_bytes: multipart.partSizeBytes,
        parts: sessionRuntimeParts.map((part) => ({
          part_number: part.partNumber,
          method: "PUT",
          url: part.url,
          headers: part.headers,
        })),
      },
    };
  }
}

function validateBundleManifest(input: unknown): Omit<DeploymentUploadInput, "api" | "sessionRuntime"> & {
  api: Omit<DeploymentUploadInput["api"], "objectKey">;
  sessionRuntime: Omit<DeploymentUploadInput["sessionRuntime"], "objectKey">;
} {
  if (!plainObject(input) || !onlyKeys(input, ["api", "session_runtime"]) ||
      !plainObject(input.api) || !onlyKeys(input.api, ["digest", "size_bytes", "manifest"]) ||
      !plainObject(input.session_runtime) || !onlyKeys(input.session_runtime, ["digest", "size_bytes", "media_type", "manifest"])) {
    throw new TypeError("invalid deployment bundle manifest");
  }
  const api = input.api as Record<string, unknown>;
  const sessionRuntime = input.session_runtime as Record<string, unknown>;
  // Only SDK 1.0 bundles deploy: a generated Edge manifest paired with its
  // Session runtime manifest.
  let manifest;
  let runtimeManifest;
  try {
    manifest = parseAPIArtifactManifest(api.manifest);
    runtimeManifest = parseSessionRuntimeManifest(sessionRuntime.manifest);
    validateArtifactPair(manifest, runtimeManifest);
  } catch {
    throw new TypeError("invalid deployment bundle manifest");
  }
  if (typeof api.digest !== "string" || !digestPattern.test(api.digest) ||
      !Number.isInteger(api.size_bytes) || (api.size_bytes as number) < 1 || (api.size_bytes as number) > 1 << 20 ||
      typeof sessionRuntime.digest !== "string" || !digestPattern.test(sessionRuntime.digest) ||
      !Number.isSafeInteger(sessionRuntime.size_bytes) || (sessionRuntime.size_bytes as number) < 1 ||
      (sessionRuntime.size_bytes as number) > 20 * 1024 ** 3 || sessionRuntime.media_type !== sessionRuntimeMediaType) {
    throw new TypeError("invalid deployment bundle manifest");
  }
  return {
    api: {
      digest: api.digest,
      sizeBytes: api.size_bytes as number,
      manifest,
    },
    sessionRuntime: {
      digest: sessionRuntime.digest,
      sizeBytes: sessionRuntime.size_bytes as number,
      mediaType: sessionRuntimeMediaType,
      manifest: runtimeManifest,
    },
  };
}

function validateCompletedParts(input: unknown): CompletedUploadPart[] {
  if (!plainObject(input) || !onlyKeys(input, ["session_runtime_parts"]) || !Array.isArray(input.session_runtime_parts) ||
      input.session_runtime_parts.length < 1 || input.session_runtime_parts.length > 10_000) {
    throw new TypeError("invalid completed upload");
  }
  return input.session_runtime_parts.map((part, index) => {
    if (!plainObject(part) || !onlyKeys(part, ["part_number", "etag"]) || part.part_number !== index + 1 ||
        typeof part.etag !== "string" || !/^"?[A-Za-z0-9+/=_-]{1,128}"?$/.test(part.etag)) {
      throw new TypeError("invalid completed upload");
    }
    return { partNumber: part.part_number, etag: part.etag };
  });
}

function publicUploadSummary(upload: DeploymentUpload) {
  return {
    id: upload.id,
    app_id: upload.appID,
    status: upload.status,
    expires_at: upload.expiresAt.toISOString(),
  };
}

function publicFinalizedUpload(upload: DeploymentUpload): PublicDeploymentUpload {
  if (!upload.releaseID) throw new ControlError("conflict");
  return {
    id: upload.id,
    app_id: upload.appID,
    status: "finalized",
    expires_at: upload.expiresAt.toISOString(),
    release_id: upload.releaseID,
  };
}

export function publicRelease(release: ProductRelease): PublicRelease {
  const component = (kind: "api" | "session_runtime"): PublicReleaseComponent => ({
    status: release.components[kind].status,
    attempts: release.components[kind].attempts,
    progress: release.components[kind].progress,
    updated_at: release.components[kind].updatedAt?.toISOString(),
    next_attempt_at: release.components[kind].nextAttemptAt?.toISOString(),
    ...(release.components[kind].errorCode ? { error_code: release.components[kind].errorCode } : {}),
  });
  return {
    id: release.id,
    app_id: release.appID,
    version: release.version,
    status: release.status,
    api_artifact_digest: release.apiArtifactDigest,
    session_runtime_artifact_digest: release.sessionRuntimeArtifactDigest,
    components: { api: component("api"), session_runtime: component("session_runtime") },
    ...(release.errorCode ? { error_code: release.errorCode } : {}),
    created_at: release.createdAt.toISOString(),
    updated_at: release.updatedAt.toISOString(),
    ...(release.activatedAt ? { activated_at: release.activatedAt.toISOString() } : {}),
    ...(release.definition ? { definition: release.definition } : {}),
  };
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function onlyKeys(value: Record<string, unknown>, keys: string[]): boolean {
  const actual = Object.keys(value).sort();
  return actual.length === keys.length && actual.every((key, index) => key === [...keys].sort()[index]);
}
