import {
  canonical,
  parseSessionRuntimeManifest,
  validateArtifactPair,
  type SessionRuntimeManifest,
} from "../api-deployments/protocol-artifact";
import { and, eq } from "drizzle-orm";

import {
  sameAPIArtifactManifest,
  validAPIArtifactManifest,
  type APIArtifactManifest,
} from "../api-deployments/artifact";
import { ControlError } from "../domain/errors";
import { assertResourceID } from "../domain/ids";
import { lockOwnedApp } from "./app-access";
import { controlQueryBuilder, executeDrizzle, withTransaction, type SQLPool } from "./database";
import { deploymentUploads } from "./schema";

const idempotencyKeyPattern = /^[A-Za-z0-9_.:-]{16,128}$/;
const digestPattern = /^sha256:[0-9a-f]{64}$/;
const storeUploadIDPattern = /^[^\0\r\n]{1,1024}$/;

export interface DeploymentUploadInput {
  api: {
    digest: string;
    objectKey: string;
    sizeBytes: number;
    manifest: APIArtifactManifest;
  };
  sessionRuntime: {
 manifest?: SessionRuntimeManifest;
    digest: string;
    objectKey: string;
    sizeBytes: number;
    mediaType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip";
  };
}

export interface DeploymentUpload {
  id: string;
  ownerUserID: string;
  appID: string;
  idempotencyKey: string;
  status: "preparing" | "uploading" | "finalized" | "expired" | "cancelled";
  input: DeploymentUploadInput;
  storeUploadID?: string;
  sessionRuntimePartSizeBytes?: number;
  sessionRuntimePartCount?: number;
  releaseID?: string;
  expiresAt: Date;
  createdAt: Date;
  updatedAt: Date;
}

interface UploadRow extends Record<string, unknown> {
  id: string;
  owner_user_id: string;
  app_id: string;
  idempotency_key: string;
  status: DeploymentUpload["status"];
  api_digest: string;
  api_object_key: string;
  api_size_bytes: number;
  api_manifest: APIArtifactManifest;
  session_runtime_digest: string;
  session_runtime_object_key: string;
  session_runtime_size_bytes: string | number;
  session_runtime_manifest?: SessionRuntimeManifest | null;
  session_runtime_media_type: DeploymentUploadInput["sessionRuntime"]["mediaType"];
  session_runtime_store_upload_id: string | null;
  session_runtime_part_size_bytes: number | null;
  session_runtime_part_count: number | null;
  release_id: string | null;
  expires_at: Date;
  created_at: Date;
  updated_at: Date;
}

const projection = `id, owner_user_id, app_id, idempotency_key, status,
       api_digest, api_object_key, api_size_bytes, api_manifest,
       session_runtime_digest, session_runtime_object_key, session_runtime_size_bytes, session_runtime_media_type, session_runtime_manifest,
       session_runtime_store_upload_id, session_runtime_part_size_bytes, session_runtime_part_count,
       release_id, expires_at, created_at, updated_at`;

const uploadSelection = {
  id: deploymentUploads.id,
  owner_user_id: deploymentUploads.ownerUserId,
  app_id: deploymentUploads.appId,
  idempotency_key: deploymentUploads.idempotencyKey,
  status: deploymentUploads.status,
  api_digest: deploymentUploads.apiDigest,
  api_object_key: deploymentUploads.apiObjectKey,
  api_size_bytes: deploymentUploads.apiSizeBytes,
  api_manifest: deploymentUploads.apiManifest,
  session_runtime_digest: deploymentUploads.sessionRuntimeDigest,
  session_runtime_object_key: deploymentUploads.sessionRuntimeObjectKey,
  session_runtime_size_bytes: deploymentUploads.sessionRuntimeSizeBytes,
  session_runtime_media_type: deploymentUploads.sessionRuntimeMediaType,
 session_runtime_manifest:deploymentUploads.sessionRuntimeManifest,
  session_runtime_store_upload_id: deploymentUploads.sessionRuntimeStoreUploadId,
  session_runtime_part_size_bytes: deploymentUploads.sessionRuntimePartSizeBytes,
  session_runtime_part_count: deploymentUploads.sessionRuntimePartCount,
  release_id: deploymentUploads.releaseId,
  expires_at: deploymentUploads.expiresAt,
  created_at: deploymentUploads.createdAt,
  updated_at: deploymentUploads.updatedAt,
};

export class DeploymentUploadRepository {
  constructor(private readonly database: SQLPool, private readonly now: () => Date = () => new Date()) {}

  async begin(
    uploadID: string,
    ownerUserID: string,
    appID: string,
    input: DeploymentUploadInput,
    idempotencyKey: string,
    expiresAt: Date,
  ): Promise<DeploymentUpload> {
    assertResourceID("deploymentUpload", uploadID);
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    validateInput(uploadID, input);
    const now = this.now();
    if (!idempotencyKeyPattern.test(idempotencyKey) || !Number.isFinite(expiresAt.getTime()) ||
        expiresAt.getTime() < now.getTime() + 60_000 || expiresAt.getTime() > now.getTime() + 60 * 60_000) {
      throw new TypeError("invalid deployment upload");
    }
    return withTransaction(this.database, async (client) => {
      const replay = await client.query<UploadRow>(`SELECT ${projection}
FROM cantelop_control.deployment_uploads
WHERE owner_user_id = $1 AND idempotency_key = $2`, [ownerUserID, idempotencyKey]);
      if (replay.rowCount === 1) {
        const upload = mapUpload(replay.rows[0]);
        if (upload.appID !== appID || !sameInput(upload.input, input)) throw new ControlError("conflict");
        return upload;
      }
      if (replay.rowCount !== 0) throw new Error("deployment upload replay returned an invalid row count");

      await lockOwnedApp(client, appID, ownerUserID, "update_app");
      await client.query(`UPDATE cantelop_control.deployment_uploads
SET status = 'expired', updated_at = $2
WHERE app_id = $1 AND status IN ('preparing', 'uploading') AND expires_at <= $2`, [appID, now]);
      const pending = await client.query(`SELECT 1 FROM cantelop_control.deployment_uploads
WHERE app_id = $1 AND status IN ('preparing', 'uploading')`, [appID]);
      if (pending.rowCount !== 0) throw new ControlError("conflict");
      const result = await client.query<UploadRow>(`INSERT INTO cantelop_control.deployment_uploads
  (id, owner_user_id, app_id, idempotency_key, status,
   api_digest, api_object_key, api_size_bytes, api_manifest,
   session_runtime_digest, session_runtime_object_key, session_runtime_size_bytes, session_runtime_media_type, session_runtime_manifest,
   expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'preparing', $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $15::jsonb, $13, $14, $14)
RETURNING ${projection}`, [
        uploadID, ownerUserID, appID, idempotencyKey,
        input.api.digest, input.api.objectKey, input.api.sizeBytes, JSON.stringify(input.api.manifest),
        input.sessionRuntime.digest, input.sessionRuntime.objectKey, input.sessionRuntime.sizeBytes, input.sessionRuntime.mediaType,
        expiresAt, now, input.sessionRuntime.manifest ? JSON.stringify(input.sessionRuntime.manifest):null,
      ]);
      return mapUpload(requiredRow(result, "create deployment upload"));
    });
  }

  async markReady(
    ownerUserID: string,
    appID: string,
    uploadID: string,
    storeUploadID: string,
    partSizeBytes: number,
    partCount: number,
  ): Promise<DeploymentUpload> {
    validateIdentity(ownerUserID, appID, uploadID);
    if (!storeUploadIDPattern.test(storeUploadID) || !Number.isInteger(partSizeBytes) ||
        partSizeBytes < 5 << 20 || partSizeBytes > 512 << 20 ||
        !Number.isInteger(partCount) || partCount < 1 || partCount > 10_000) {
      throw new TypeError("invalid deployment upload readiness");
    }
    const result = await this.database.query<UploadRow>(`UPDATE cantelop_control.deployment_uploads
SET status = 'uploading', session_runtime_store_upload_id = $4,
    session_runtime_part_size_bytes = $5, session_runtime_part_count = $6, updated_at = $7
WHERE id = $1 AND owner_user_id = $2 AND app_id = $3 AND status = 'preparing' AND expires_at > $7
RETURNING ${projection}`, [uploadID, ownerUserID, appID, storeUploadID, partSizeBytes, partCount, this.now()]);
    if (result.rowCount === 1) return mapUpload(result.rows[0]);
    const replay = await this.getForUser(ownerUserID, appID, uploadID);
    if (replay.status !== "uploading" || replay.storeUploadID !== storeUploadID ||
        replay.sessionRuntimePartSizeBytes !== partSizeBytes || replay.sessionRuntimePartCount !== partCount) throw new ControlError("conflict");
    return replay;
  }

  async getForUser(ownerUserID: string, appID: string, uploadID: string): Promise<DeploymentUpload> {
    validateIdentity(ownerUserID, appID, uploadID);
    const db = controlQueryBuilder(this.database);
    const result = await executeDrizzle(this.database, db.select(uploadSelection)
      .from(deploymentUploads)
      .where(and(
        eq(deploymentUploads.id, uploadID),
        eq(deploymentUploads.ownerUserId, ownerUserID),
        eq(deploymentUploads.appId, appID),
      )));
    if (result.rowCount !== 1) throw new ControlError("not_found");
    return mapUpload(result.rows[0]);
  }

  async listPendingForUser(ownerUserID: string, appID: string): Promise<DeploymentUpload[]> {
    assertResourceID("user", ownerUserID);
    assertResourceID("app", appID);
    const result = await this.database.query<UploadRow>(`SELECT ${projection}
FROM cantelop_control.deployment_uploads
WHERE owner_user_id = $1 AND app_id = $2 AND status IN ('preparing', 'uploading')
ORDER BY created_at, id`, [ownerUserID, appID]);
    return result.rows.map(mapUpload);
  }

  async cancelForUser(ownerUserID: string, appID: string, uploadID: string): Promise<DeploymentUpload> {
    validateIdentity(ownerUserID, appID, uploadID);
    return withTransaction(this.database, async (client) => {
      // Serialize with begin/release creation; never cancel a replacement upload.
      const app = await client.query(`SELECT id FROM cantelop_control.apps
WHERE id = $1 AND owner_user_id = $2 FOR UPDATE`, [appID, ownerUserID]);
      if (app.rowCount !== 1) throw new ControlError("not_found");
      const result = await client.query<UploadRow>(`SELECT ${projection}
FROM cantelop_control.deployment_uploads
WHERE id = $1 AND owner_user_id = $2 AND app_id = $3 FOR UPDATE`, [uploadID, ownerUserID, appID]);
      if (result.rowCount !== 1) throw new ControlError("not_found");
      const upload = mapUpload(result.rows[0]);
      if (upload.status === "cancelled") return upload;
      if (upload.status === "finalized") throw new ControlError("conflict");
      // Also protect releases created before finalization became atomic.
      const release = await client.query(`SELECT 1 FROM cantelop_control.releases
WHERE owner_user_id = $1 AND idempotency_key = $2`, [ownerUserID, upload.idempotencyKey]);
      if (release.rowCount !== 0) throw new ControlError("conflict");
      const cancelled = await client.query<UploadRow>(`UPDATE cantelop_control.deployment_uploads
SET status = 'cancelled', updated_at = $4
WHERE id = $1 AND owner_user_id = $2 AND app_id = $3
RETURNING ${projection}`, [uploadID, ownerUserID, appID, this.now()]);
      return mapUpload(requiredRow(cancelled, "cancel deployment upload"));
    });
  }
}

function validateIdentity(ownerUserID: string, appID: string, uploadID: string): void {
  assertResourceID("user", ownerUserID);
  assertResourceID("app", appID);
  assertResourceID("deploymentUpload", uploadID);
}

function validateInput(uploadID: string, input: DeploymentUploadInput): void {
  validateArtifactPair(input.api.manifest, parseSessionRuntimeManifest(input.sessionRuntime.manifest));
  const suffix = uploadID.slice("upl_".length);
  if (!digestPattern.test(input.api.digest) || input.api.objectKey !== `deployment-uploads/${suffix}/api/worker.mjs` ||
      !Number.isInteger(input.api.sizeBytes) || input.api.sizeBytes < 1 || input.api.sizeBytes > 1 << 20 ||
      !validAPIArtifactManifest(input.api.manifest) ||
      !digestPattern.test(input.sessionRuntime.digest) || input.sessionRuntime.objectKey !== `deployment-uploads/${suffix}/session-runtime/image.tar.gz` ||
      !Number.isSafeInteger(input.sessionRuntime.sizeBytes) || input.sessionRuntime.sizeBytes < 1 || input.sessionRuntime.sizeBytes > 20 * 1024 ** 3 ||
      input.sessionRuntime.mediaType !== "application/vnd.cantelop.session-runtime.image.v1.tar+gzip") {
    throw new TypeError("invalid deployment upload input");
  }
}

function sameInput(left: DeploymentUploadInput, right: DeploymentUploadInput): boolean {
  // Object keys are allocated from a fresh candidate ID on every begin request.
  // Replay compares customer input, then reuses the originally stored keys.
  return left.api.digest === right.api.digest &&
    left.api.sizeBytes === right.api.sizeBytes && sameAPIArtifactManifest(left.api.manifest, right.api.manifest) &&
    canonical(left.sessionRuntime.manifest ?? null) === canonical(right.sessionRuntime.manifest ?? null) &&
    left.sessionRuntime.digest === right.sessionRuntime.digest &&
    left.sessionRuntime.sizeBytes === right.sessionRuntime.sizeBytes && left.sessionRuntime.mediaType === right.sessionRuntime.mediaType;
}

function requiredRow<Row>(result: { rows: Row[]; rowCount: number | null }, operation: string): Row {
  if (result.rowCount !== 1 || result.rows.length !== 1) throw new Error(`${operation} returned an invalid row count`);
  return result.rows[0];
}

function mapUpload(row: UploadRow): DeploymentUpload {
  return {
    id: row.id,
    ownerUserID: row.owner_user_id,
    appID: row.app_id,
    idempotencyKey: row.idempotency_key,
    status: row.status,
    input: {
      api: {
        digest: row.api_digest,
        objectKey: row.api_object_key,
        sizeBytes: row.api_size_bytes,
        manifest: row.api_manifest,
      },
      sessionRuntime: {
        digest: row.session_runtime_digest,
        objectKey: row.session_runtime_object_key,
        sizeBytes: Number(row.session_runtime_size_bytes),
        mediaType: row.session_runtime_media_type,
        ...(row.session_runtime_manifest ? { manifest: parseSessionRuntimeManifest(row.session_runtime_manifest) } : {}),
      },
    },
    ...(row.session_runtime_store_upload_id ? { storeUploadID: row.session_runtime_store_upload_id } : {}),
    ...(row.session_runtime_part_size_bytes ? { sessionRuntimePartSizeBytes: row.session_runtime_part_size_bytes } : {}),
    ...(row.session_runtime_part_count ? { sessionRuntimePartCount: row.session_runtime_part_count } : {}),
    ...(row.release_id ? { releaseID: row.release_id } : {}),
    expiresAt: row.expires_at,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}
