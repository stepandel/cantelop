import { createHash } from "node:crypto";
import { open, stat } from "node:fs/promises";
import {
  AbortMultipartUploadCommand, CompleteMultipartUploadCommand, CreateMultipartUploadCommand,
  GetObjectCommand, HeadObjectCommand, PutObjectCommand, S3Client, UploadPartCommand,
} from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

const partBytes = 64 << 20;
const digestPattern = /^[0-9a-f]{64}$/;
const maxImageBytes = 52 * 1024 ** 3;

export interface PreparedManifest {
  schema_version: 1;
  build_id: string;
  image_key: string;
  rootfs_sha256: string;
  rootfs_size_bytes: number;
  process: {
    args: string[];
    env: string[];
    working_directory: string;
    uid: number;
    gid: number;
  };
}

export interface PreparedArtifactStore {
  uploadImage(path: string, digest: string, sizeBytes: number): Promise<string>;
  uploadManifest(manifest: PreparedManifest): Promise<{ key: string; digest: string }>;
  signImageDownload(key: string, expiresInSeconds: number): Promise<string>;
  readManifest(key: string, digest: string): Promise<PreparedManifest>;
}

export class S3PreparedArtifactStore implements PreparedArtifactStore {
  constructor(private readonly client: S3Client, private readonly bucket: string) {
    if (!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(bucket)) throw new TypeError("invalid artifact bucket");
  }

  async uploadImage(path: string, digest: string, sizeBytes: number): Promise<string> {
    if (!digestPattern.test(digest) || !Number.isSafeInteger(sizeBytes) || sizeBytes < 1 || sizeBytes > maxImageBytes) {
      throw new TypeError("invalid prepared image identity");
    }
    if ((await stat(path)).size !== sizeBytes) throw new Error("prepared image size changed before upload");
    const key = imageKey(digest);
    if (await this.matches(key, digest, sizeBytes)) return key;
    const created = await this.client.send(new CreateMultipartUploadCommand({
      Bucket: this.bucket, Key: key, ContentType: "application/vnd.cantelop.rootfs.ext4",
      Metadata: { "cantelop-sha256": digest },
    }));
    if (!created.UploadId) throw new Error("R2 returned no multipart upload ID");
    const uploadID = created.UploadId;
    try {
      const file = await open(path, "r");
      const parts: { PartNumber: number; ETag: string }[] = [];
      const hash = createHash("sha256");
      try {
        for (let offset = 0, partNumber = 1; offset < sizeBytes; offset += partBytes, partNumber++) {
          const length = Math.min(partBytes, sizeBytes - offset);
          const body = Buffer.allocUnsafe(length);
          let filled = 0;
          while (filled < length) {
            const { bytesRead } = await file.read(body, filled, length - filled, offset + filled);
            if (bytesRead === 0) throw new Error("prepared image truncated during upload");
            filled += bytesRead;
          }
          hash.update(body);
          const uploaded = await this.client.send(new UploadPartCommand({
            Bucket: this.bucket, Key: key, UploadId: uploadID, PartNumber: partNumber, Body: body,
          }));
          if (!uploaded.ETag) throw new Error("R2 returned no uploaded-part ETag");
          parts.push({ PartNumber: partNumber, ETag: uploaded.ETag });
        }
      } finally {
        await file.close();
      }
      if (hash.digest("hex") !== digest) throw new Error("prepared image changed during upload");
      await this.client.send(new CompleteMultipartUploadCommand({
        Bucket: this.bucket, Key: key, UploadId: uploadID, MultipartUpload: { Parts: parts },
      }));
    } catch (error) {
      await this.client.send(new AbortMultipartUploadCommand({ Bucket: this.bucket, Key: key, UploadId: uploadID })).catch(() => {});
      throw error;
    }
    if (!(await this.matches(key, digest, sizeBytes))) throw new Error("prepared image upload verification failed");
    return key;
  }

  async uploadManifest(manifest: PreparedManifest): Promise<{ key: string; digest: string }> {
    if (manifest.schema_version !== 1 || !/^sha256:[0-9a-f]{64}$/.test(manifest.build_id) ||
        !digestPattern.test(manifest.rootfs_sha256) || manifest.image_key !== imageKey(manifest.rootfs_sha256)) {
      throw new TypeError("invalid prepared manifest");
    }
    const document = Buffer.from(JSON.stringify(manifest));
    if (document.length > 1 << 20) throw new TypeError("prepared manifest exceeds size limit");
    const digest = createHash("sha256").update(document).digest("hex");
    const key = `prepared-images/manifests/${digest}.json`;
    if (!(await this.matches(key, digest, document.length))) {
      try {
        await this.client.send(new PutObjectCommand({
          Bucket: this.bucket, Key: key, Body: document, ContentLength: document.length,
          ContentType: "application/vnd.cantelop.prepared-image.v1+json",
          Metadata: { "cantelop-sha256": digest }, IfNoneMatch: "*",
        }));
      } catch (error) {
        if (!(await this.matches(key, digest, document.length))) throw error;
      }
    }
    if (!(await this.matches(key, digest, document.length))) throw new Error("prepared manifest upload verification failed");
    return { key, digest: `sha256:${digest}` };
  }

  async signImageDownload(key: string, expiresInSeconds: number): Promise<string> {
    if (!/^prepared-images\/sha256\/[0-9a-f]{64}\.ext4$/.test(key) ||
        !Number.isInteger(expiresInSeconds) || expiresInSeconds < 60 || expiresInSeconds > 3600) {
      throw new TypeError("invalid prepared image download request");
    }
    return getSignedUrl(this.client, new GetObjectCommand({ Bucket: this.bucket, Key: key }), { expiresIn: expiresInSeconds });
  }

  async readManifest(key: string, digest: string): Promise<PreparedManifest> {
    if (!/^prepared-images\/manifests\/[0-9a-f]{64}\.json$/.test(key) ||
        !/^sha256:[0-9a-f]{64}$/.test(digest) || key !== `prepared-images/manifests/${digest.slice(7)}.json`) {
      throw new TypeError("invalid prepared manifest identity");
    }
    const response = await this.client.send(new GetObjectCommand({ Bucket: this.bucket, Key: key }));
    if (!response.Body || (response.ContentLength ?? 0) > 1 << 20) throw new Error("prepared manifest is missing or oversized");
    const chunks: Buffer[] = [];
    let length = 0;
    const reader = response.Body.transformToWebStream().getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        const bytes = Buffer.from(value);
        length += bytes.length;
        if (length > 1 << 20) throw new Error("prepared manifest is oversized");
        chunks.push(bytes);
      }
    } finally {
      reader.releaseLock();
    }
    const bytes = Buffer.concat(chunks, length);
    if (bytes.length > 1 << 20 || createHash("sha256").update(bytes).digest("hex") !== digest.slice(7)) {
      throw new Error("prepared manifest checksum mismatch");
    }
    const value: unknown = JSON.parse(bytes.toString("utf8"));
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("invalid prepared manifest");
    const manifest = value as Record<string, unknown>;
    if (manifest.schema_version !== 1 || !/^sha256:[0-9a-f]{64}$/.test(String(manifest.build_id)) ||
        !/^[0-9a-f]{64}$/.test(String(manifest.rootfs_sha256)) ||
        manifest.image_key !== `prepared-images/sha256/${manifest.rootfs_sha256}.ext4` ||
        !Number.isSafeInteger(manifest.rootfs_size_bytes) || (manifest.rootfs_size_bytes as number) < 1 ||
        (manifest.rootfs_size_bytes as number) > maxImageBytes || !manifest.process ||
        typeof manifest.process !== "object" || Array.isArray(manifest.process)) {
      throw new Error("invalid prepared manifest");
    }
    return manifest as unknown as PreparedManifest;
  }

  private async matches(key: string, digest: string, sizeBytes: number): Promise<boolean> {
    try {
      const result = await this.client.send(new HeadObjectCommand({ Bucket: this.bucket, Key: key }));
      if (result.ContentLength !== sizeBytes || result.Metadata?.["cantelop-sha256"] !== digest) {
        throw new Error(`conflicting prepared artifact at ${key}`);
      }
      return true;
    } catch (error) {
      const candidate = error as { name?: string; $metadata?: { httpStatusCode?: number } };
      if (candidate.name === "NotFound" || candidate.$metadata?.httpStatusCode === 404) return false;
      throw error;
    }
  }
}

function imageKey(digest: string): string {
  return `prepared-images/sha256/${digest}.ext4`;
}

export function configuredPreparedArtifactStore(environment: NodeJS.ProcessEnv = process.env): S3PreparedArtifactStore {
  const required = (name: string): string => {
    const value = environment[name]?.trim();
    if (!value) throw new Error(`${name} is required`);
    return value;
  };
  const endpoint = new URL(required("CANTELOP_ARTIFACT_ENDPOINT"));
  if (endpoint.protocol !== "https:" || endpoint.username || endpoint.password || endpoint.search || endpoint.hash || endpoint.pathname !== "/") {
    throw new Error("CANTELOP_ARTIFACT_ENDPOINT must be an HTTPS origin");
  }
  return new S3PreparedArtifactStore(new S3Client({
    endpoint: endpoint.origin, region: environment.CANTELOP_ARTIFACT_REGION?.trim() || "auto", forcePathStyle: true,
    credentials: {
      accessKeyId: required("CANTELOP_ARTIFACT_ACCESS_KEY_ID"),
      secretAccessKey: required("CANTELOP_ARTIFACT_SECRET_ACCESS_KEY"),
    },
  }), required("CANTELOP_ARTIFACT_BUCKET"));
}
