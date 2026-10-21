import {
  AbortMultipartUploadCommand,
  CompleteMultipartUploadCommand,
  CreateMultipartUploadCommand,
  DeleteObjectCommand,
  GetObjectCommand,
  HeadObjectCommand,
  ListPartsCommand,
  PutObjectCommand,
  S3Client,
  UploadPartCommand,
} from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { ControlError } from "../domain/errors";

import type {
  CompletedUploadPart,
  DeploymentArtifactStore,
  MultipartUpload,
  MultipartUploadPartTarget,
  PresignedUploadTarget,
} from "./artifact-store";

const sessionRuntimePartSizeBytes = 64 << 20;
const maxSessionRuntimeBytes = 20 * 1024 ** 3;
const digestPattern = /^sha256:[0-9a-f]{64}$/;
const apiKeyPattern = /^deployment-uploads\/[0-9a-f]{32}\/api\/worker\.mjs$/;
const sessionRuntimeKeyPattern = /^deployment-uploads\/[0-9a-f]{32}\/session-runtime\/image\.tar\.gz$/;
const storeUploadIDPattern = /^[^\0\r\n]{1,1024}$/;
const etagPattern = /^"?[A-Za-z0-9+/=_-]{1,128}"?$/;

type StoreCommand = AbortMultipartUploadCommand | CompleteMultipartUploadCommand |
  CreateMultipartUploadCommand | DeleteObjectCommand | HeadObjectCommand | ListPartsCommand;

interface S3Sender {
  send(command: StoreCommand): Promise<unknown>;
}

type URLSigner = (
  client: S3Sender,
  command: GetObjectCommand | PutObjectCommand | UploadPartCommand,
  expiresInSeconds: number,
) => Promise<string>;

export class S3DeploymentArtifactStore implements DeploymentArtifactStore {
  constructor(
    private readonly client: S3Sender,
    private readonly bucket: string,
    private readonly signURL: URLSigner = defaultSigner,
  ) {
    if (!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(bucket)) throw new TypeError("invalid artifact bucket");
  }

  async createSessionRuntimeUpload(objectKey: string, sizeBytes: number, digest: string): Promise<MultipartUpload> {
    validateSessionRuntime(objectKey, sizeBytes, digest);
    const result = await this.client.send(new CreateMultipartUploadCommand({
      Bucket: this.bucket,
      Key: objectKey,
      ContentType: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip",
      Metadata: { "cantelop-sha256": digest.slice("sha256:".length) },
    })) as { UploadId?: string };
    if (!result.UploadId || !storeUploadIDPattern.test(result.UploadId)) throw new Error("artifact store returned an invalid upload ID");
    return {
      storeUploadID: result.UploadId,
      partSizeBytes: sessionRuntimePartSizeBytes,
      partCount: Math.ceil(sizeBytes / sessionRuntimePartSizeBytes),
    };
  }

  async abortSessionRuntimeUpload(objectKey: string, storeUploadID: string): Promise<void> {
    validateSessionRuntimeKeyAndUploadID(objectKey, storeUploadID);
    try {
      await this.client.send(new AbortMultipartUploadCommand({
        Bucket: this.bucket,
        Key: objectKey,
        UploadId: storeUploadID,
      }));
    } catch (error) {
      if ((error as { name?: string }).name !== "NoSuchUpload") throw error;
    }
  }

  async deleteSessionRuntimeObject(objectKey: string): Promise<void> {
    if (!sessionRuntimeKeyPattern.test(objectKey)) throw new TypeError("invalid Session runtime object key");
    await this.client.send(new DeleteObjectCommand({ Bucket: this.bucket, Key: objectKey }));
  }

  async signAPIUpload(
    objectKey: string,
    sizeBytes: number,
    digest: string,
    expiresInSeconds: number,
  ): Promise<PresignedUploadTarget> {
    validateAPI(objectKey, sizeBytes, digest);
    validateExpiry(expiresInSeconds);
    const checksum = Buffer.from(digest.slice("sha256:".length), "hex").toString("base64");
    const command = new PutObjectCommand({
      Bucket: this.bucket,
      Key: objectKey,
      ContentLength: sizeBytes,
      ContentType: "application/javascript+module",
      ChecksumSHA256: checksum,
      Metadata: { "cantelop-sha256": digest.slice("sha256:".length) },
    });
    return {
      url: await this.signURL(this.client, command, expiresInSeconds),
      headers: Object.freeze({
        "content-type": "application/javascript+module",
        "x-amz-checksum-sha256": checksum,
        "x-amz-meta-cantelop-sha256": digest.slice("sha256:".length),
      }),
    };
  }

  async signSessionRuntimeParts(
    objectKey: string,
    upload: MultipartUpload,
    sizeBytes: number,
    expiresInSeconds: number,
  ): Promise<MultipartUploadPartTarget[]> {
    validateSessionRuntimeKeyAndUploadID(objectKey, upload.storeUploadID);
    validateMultipartSize(upload, sizeBytes);
    validateExpiry(expiresInSeconds);
    return Promise.all(Array.from({ length: upload.partCount }, async (_, index) => {
      const partNumber = index + 1;
      const command = new UploadPartCommand({
        Bucket: this.bucket,
        Key: objectKey,
        UploadId: upload.storeUploadID,
        PartNumber: partNumber,
        ContentLength: partLength(upload.partSizeBytes, sizeBytes, index),
      });
      return {
        partNumber,
        url: await this.signURL(this.client, command, expiresInSeconds),
        headers: Object.freeze({}),
      };
    }));
  }

  async completeSessionRuntimeUpload(
    objectKey: string,
    storeUploadID: string,
    parts: CompletedUploadPart[],
    sizeBytes: number,
    partSizeBytes: number,
  ): Promise<void> {
    validateSessionRuntimeKeyAndUploadID(objectKey, storeUploadID);
    validateMultipartSize({ storeUploadID, partSizeBytes, partCount: parts.length }, sizeBytes);
    if (parts.some((part, index) => part.partNumber !== index + 1 || !etagPattern.test(part.etag))) {
      throw new TypeError("invalid completed sessionRuntime parts");
    }
    let marker: string | undefined;
    let index = 0;
    do {
      let listed: { Parts?: Array<{ PartNumber?: number; ETag?: string; Size?: number }>;
        IsTruncated?: boolean; NextPartNumberMarker?: string };
      try {
        listed = await this.client.send(new ListPartsCommand({
          Bucket: this.bucket, Key: objectKey, UploadId: storeUploadID,
          MaxParts: 1000, ...(marker === undefined ? {} : { PartNumberMarker: marker }),
        })) as typeof listed;
      } catch (error) {
        // A completed or aborted upload cannot be finalized again.
        if ((error as { name?: string }).name === "NoSuchUpload") throw new ControlError("conflict");
        throw error;
      }
      for (const part of listed.Parts ?? []) {
        const expected = parts[index];
        // Leave the multipart upload open so the client can re-upload a part and retry.
        if (!expected || part.PartNumber !== expected.partNumber || unquote(part.ETag) !== unquote(expected.etag) ||
            part.Size !== partLength(partSizeBytes, sizeBytes, index)) throw new ControlError("conflict");
        index++;
      }
      if (!listed.IsTruncated) break;
      if (!listed.NextPartNumberMarker || !/^[1-9][0-9]*$/.test(listed.NextPartNumberMarker) ||
          (marker !== undefined && Number(listed.NextPartNumberMarker) <= Number(marker))) {
        throw new Error("artifact store returned an invalid part listing");
      }
      marker = listed.NextPartNumberMarker;
    } while (true);
    if (index !== parts.length) throw new ControlError("conflict");
    await this.client.send(new CompleteMultipartUploadCommand({
      Bucket: this.bucket,
      Key: objectKey,
      UploadId: storeUploadID,
      MultipartUpload: { Parts: parts.map((part) => ({ PartNumber: part.partNumber, ETag: part.etag })) },
    }));
  }

  async inspectObject(
    objectKey: string,
    sizeBytes: number,
    digest: string,
  ): Promise<"missing" | "matches" | "mismatch"> {
    if ((!apiKeyPattern.test(objectKey) && !sessionRuntimeKeyPattern.test(objectKey)) ||
        !Number.isSafeInteger(sizeBytes) || sizeBytes < 1 || sizeBytes > maxSessionRuntimeBytes || !digestPattern.test(digest)) {
      throw new TypeError("invalid artifact identity");
    }
    try {
      const result = await this.client.send(new HeadObjectCommand({ Bucket: this.bucket, Key: objectKey })) as {
        ContentLength?: number;
        Metadata?: Record<string, string | undefined>;
      };
      return result.ContentLength === sizeBytes && result.Metadata?.["cantelop-sha256"] === digest.slice("sha256:".length)
        ? "matches" : "mismatch";
    } catch (error) {
      const candidate = error as { name?: string; $metadata?: { httpStatusCode?: number } };
      if (candidate.name === "NotFound" || candidate.$metadata?.httpStatusCode === 404) return "missing";
      throw error;
    }
  }

  async signSessionRuntimeDownload(objectKey: string, expiresInSeconds: number): Promise<string> {
    if (!sessionRuntimeKeyPattern.test(objectKey)) throw new TypeError("invalid Session runtime object key");
    validateExpiry(expiresInSeconds);
    return this.signURL(this.client, new GetObjectCommand({ Bucket: this.bucket, Key: objectKey }), expiresInSeconds);
  }
}

export function configuredDeploymentArtifactStore(
  environment: NodeJS.ProcessEnv = process.env,
): S3DeploymentArtifactStore {
  const bucket = required(environment, "CANTELOP_ARTIFACT_BUCKET");
  const endpoint = new URL(required(environment, "CANTELOP_ARTIFACT_ENDPOINT"));
  if (endpoint.protocol !== "https:" || endpoint.username || endpoint.password || endpoint.search ||
      endpoint.hash || endpoint.pathname !== "/") {
    throw new Error("CANTELOP_ARTIFACT_ENDPOINT must be an HTTPS origin");
  }
  const accessKeyId = required(environment, "CANTELOP_ARTIFACT_ACCESS_KEY_ID");
  const secretAccessKey = required(environment, "CANTELOP_ARTIFACT_SECRET_ACCESS_KEY");
  const region = environment.CANTELOP_ARTIFACT_REGION?.trim() || "auto";
  if (!/^[A-Za-z0-9][A-Za-z0-9-]{0,31}$/.test(region)) throw new Error("invalid artifact region");
  return new S3DeploymentArtifactStore(new S3Client({
    endpoint: endpoint.origin,
    region,
    // Keep every presigned artifact URL on the explicitly configured origin.
    // Fire Fuse allowlists that origin before downloading a customer Session runtime.
    forcePathStyle: true,
    credentials: { accessKeyId, secretAccessKey },
  }), bucket);
}

async function defaultSigner(
  client: S3Sender,
  command: GetObjectCommand | PutObjectCommand | UploadPartCommand,
  expiresInSeconds: number,
): Promise<string> {
  return getSignedUrl(client as S3Client, command as PutObjectCommand, {
    expiresIn: expiresInSeconds,
    // R2 accepts these values in a presigned query but does not persist the
    // object metadata from there. Keep them as signed request headers so the
    // staged object can be verified before a release is created.
    unhoistableHeaders: new Set([
      "x-amz-checksum-sha256",
      "x-amz-meta-cantelop-sha256",
    ]),
  });
}

function validateAPI(objectKey: string, sizeBytes: number, digest: string): void {
  if (!apiKeyPattern.test(objectKey) || !Number.isInteger(sizeBytes) || sizeBytes < 1 ||
      sizeBytes > 1 << 20 || !digestPattern.test(digest)) throw new TypeError("invalid API upload");
}

function validateSessionRuntime(objectKey: string, sizeBytes: number, digest: string): void {
  if (!sessionRuntimeKeyPattern.test(objectKey) || !digestPattern.test(digest)) throw new TypeError("invalid Session runtime upload");
  validateSessionRuntimeSize(sizeBytes);
}

function validateSessionRuntimeSize(sizeBytes: number): void {
  if (!Number.isSafeInteger(sizeBytes) || sizeBytes < 1 || sizeBytes > maxSessionRuntimeBytes) {
    throw new TypeError("invalid Session runtime size");
  }
}

function validateSessionRuntimeKeyAndUploadID(objectKey: string, storeUploadID: string): void {
  if (!sessionRuntimeKeyPattern.test(objectKey) || !storeUploadIDPattern.test(storeUploadID)) {
    throw new TypeError("invalid Session runtime multipart upload");
  }
}

function validateMultipart(upload: MultipartUpload): void {
  if (!Number.isInteger(upload.partSizeBytes) || upload.partSizeBytes < 5 << 20 || upload.partSizeBytes > 512 << 20 ||
      !Number.isInteger(upload.partCount) || upload.partCount < 1 || upload.partCount > 10_000) {
    throw new TypeError("invalid Session runtime multipart upload");
  }
}

function validateMultipartSize(upload: MultipartUpload, sizeBytes: number): void {
  validateMultipart(upload);
  validateSessionRuntimeSize(sizeBytes);
  if (upload.partCount !== Math.ceil(sizeBytes / upload.partSizeBytes)) throw new TypeError("invalid Session runtime multipart upload");
}

function partLength(partSizeBytes: number, sizeBytes: number, index: number): number {
  return Math.min(partSizeBytes, sizeBytes - index * partSizeBytes);
}

function unquote(etag: string | undefined): string | undefined {
  return etag?.replace(/^"|"$/g, "");
}

function validateExpiry(expiresInSeconds: number): void {
  if (!Number.isInteger(expiresInSeconds) || expiresInSeconds < 60 || expiresInSeconds > 3600) {
    throw new TypeError("invalid upload URL expiry");
  }
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
