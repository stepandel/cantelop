import { createHash } from "node:crypto";
import { createReadStream, createWriteStream } from "node:fs";
import { lstat, mkdir, open, statfs, unlink } from "node:fs/promises";
import { join } from "node:path";
import { request as httpRequest } from "node:http";
import { pipeline } from "node:stream/promises";
import { Transform } from "node:stream";
import { createGunzip } from "node:zlib";
import type { PreparedManifest } from "./prepared-artifact-store";

const incoming = "/var/lib/cantelop-builder/incoming";
const images = "/var/lib/cantelop-builder/images";
const socketPath = "/run/cantelop-builder/materializer.sock";
const maxArchiveBytes = 20 * 1024 ** 3;
const maxImageBytes = 52 * 1024 ** 3;
const operationPattern = /^op_[0-9a-f]{32}$/;
const digestPattern = /^sha256:[0-9a-f]{64}$/;

export class InvalidImageError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "InvalidImageError";
  }
}

export interface PreparedLocalImage {
  manifest: PreparedManifest;
  imagePath: string;
}

export interface ImageBuilder {
  prepare(commandID: string, sourceURL: string, expectedDigest: string, sizeBytes: number, signal?: AbortSignal): Promise<PreparedLocalImage>;
  removeImage(path: string): Promise<void>;
}

export class LocalImageBuilder implements ImageBuilder {
  constructor(private readonly request: typeof fetch = fetch) {}

  async prepare(commandID: string, sourceURL: string, expectedDigest: string, sizeBytes: number, signal?: AbortSignal): Promise<PreparedLocalImage> {
    if (!operationPattern.test(commandID) || !digestPattern.test(expectedDigest) ||
        !Number.isSafeInteger(sizeBytes) || sizeBytes < 1 || sizeBytes > maxArchiveBytes) {
      throw new TypeError("invalid source image identity");
    }
    const source = new URL(sourceURL);
    if (source.protocol !== "https:" || source.username || source.password || source.hash || !source.search) {
      throw new TypeError("invalid signed source URL");
    }
    await mkdir(incoming, { recursive: true, mode: 0o700 });
    const space = await statfs(incoming);
    if (space.bavail * space.bsize < 180 * 1024 ** 3) {
      throw new Error("builder scratch volume has insufficient free space");
    }
    const compressedPath = join(incoming, `${commandID}.tar.gz`);
    const archivePath = join(incoming, `${commandID}.tar`);
    try {
      const response = await this.request(sourceURL, { redirect: "error", signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(30 * 60_000)]) : AbortSignal.timeout(30 * 60_000) });
      if (!response.ok || !response.body || (response.headers.has("content-length") && Number(response.headers.get("content-length")) !== sizeBytes)) {
        throw new Error("source image download failed or size changed");
      }
      const compressed = await open(compressedPath, "w", 0o600);
      const hash = createHash("sha256");
      let downloaded = 0;
      const reader = response.body.getReader();
      try {
        while (true) {
          const result = await reader.read();
          if (result.done) break;
          const chunk = result.value;
          downloaded += chunk.byteLength;
          if (downloaded > sizeBytes) throw new InvalidImageError("source image exceeds declared size");
          hash.update(chunk);
          let written = 0;
          while (written < chunk.byteLength) {
            const result = await compressed.write(chunk, written, chunk.byteLength - written);
            if (result.bytesWritten === 0) throw new Error("source image write made no progress");
            written += result.bytesWritten;
          }
        }
        await compressed.sync();
      } finally {
        reader.releaseLock();
        await compressed.close();
      }
      if (downloaded !== sizeBytes) throw new Error("source image download was truncated");
      if (`sha256:${hash.digest("hex")}` !== expectedDigest) {
        throw new InvalidImageError("source image integrity mismatch");
      }
      let expanded = 0;
      const budget = new Transform({
        transform(chunk: Buffer, _encoding, callback) {
          expanded += chunk.length;
          if (expanded > maxArchiveBytes || expanded > sizeBytes * 100 + (1 << 20)) {
            callback(new InvalidImageError("expanded Docker archive exceeds size limit"));
          } else {
            callback(null, chunk);
          }
        },
      });
      try {
        await pipeline(createReadStream(compressedPath), createGunzip(), budget, createWriteStream(archivePath, { mode: 0o600 }), { signal });
      } catch (error) {
        if (error instanceof InvalidImageError) throw error;
        if (error instanceof Error && (error as NodeJS.ErrnoException).code?.startsWith("Z_")) {
          throw new InvalidImageError("invalid compressed Docker archive");
        }
        throw error;
      }
      const result: unknown = await requestConversion(commandID, signal);
      if (!isPreparedResult(result, commandID)) {
        // The builder installed an image before we rejected its result; do not strand it.
        const path = (result as { image?: { rootfs_image?: unknown } } | null)?.image?.rootfs_image;
        if (typeof path === "string" && /^\/var\/lib\/cantelop-builder\/images\/sha256-[0-9a-f]{64}(-[0-9a-f]{32})?\.ext4$/.test(path)) {
          await unlink(path).catch(() => {});
        }
        throw new Error("builder returned an invalid prepared image");
      }
      const image = result.image;
      try {
        const imageInfo = await lstat(image.rootfs_image);
        if (!imageInfo.isFile() || imageInfo.isSymbolicLink() || imageInfo.size !== image.rootfs_size_bytes) {
          throw new Error("prepared image size or file type changed");
        }
        const imageHash = createHash("sha256");
        for await (const chunk of createReadStream(image.rootfs_image)) imageHash.update(chunk);
        if (imageHash.digest("hex") !== image.rootfs_sha256) throw new Error("prepared image digest mismatch");
      } catch (error) {
        await this.removeImage(image.rootfs_image).catch(() => {});
        throw error;
      }
      return {
        imagePath: image.rootfs_image,
        manifest: {
          schema_version: 1, build_id: result.build_id,
          image_key: `prepared-images/sha256/${image.rootfs_sha256}.ext4`,
          rootfs_sha256: image.rootfs_sha256, rootfs_size_bytes: image.rootfs_size_bytes,
          process: image.process,
        },
      };
    } finally {
      await Promise.all([unlink(compressedPath).catch(() => {}), unlink(archivePath).catch(() => {})]);
    }
  }

  async removeImage(path: string): Promise<void> {
    if (!/^\/var\/lib\/cantelop-builder\/images\/sha256-[0-9a-f]{64}-[0-9a-f]{32}\.ext4$/.test(path)) {
      throw new TypeError("invalid prepared image path");
    }
    await unlink(path).catch((error: NodeJS.ErrnoException) => { if (error.code !== "ENOENT") throw error; });
  }
}

interface PreparedResult {
  schema_version: 1;
  build_id: string;
  image: {
    rootfs_image: string;
    rootfs_sha256: string;
    rootfs_size_bytes: number;
    process: PreparedManifest["process"];
  };
}

function isPreparedResult(value: unknown, commandID: string): value is PreparedResult {
  if (!plain(value) || value.schema_version !== 1 || typeof value.build_id !== "string" || !digestPattern.test(value.build_id) ||
      !plain(value.image)) return false;
  const image = value.image;
  if (typeof image.rootfs_sha256 !== "string" || !/^[0-9a-f]{64}$/.test(image.rootfs_sha256) ||
      typeof image.rootfs_image !== "string" ||
      !new RegExp(`^${images}/sha256-${image.rootfs_sha256}-[0-9a-f]{32}\\.ext4$`).test(image.rootfs_image) ||
      !Number.isSafeInteger(image.rootfs_size_bytes) || (image.rootfs_size_bytes as number) < 1 ||
      (image.rootfs_size_bytes as number) > maxImageBytes || !plain(image.process)) return false;
  const process = image.process;
  return operationPattern.test(commandID) && Array.isArray(process.args) && process.args.every((arg) => typeof arg === "string") &&
    Array.isArray(process.env) && process.env.every((entry) => typeof entry === "string") &&
    typeof process.working_directory === "string" && Number.isInteger(process.uid) && Number.isInteger(process.gid) &&
    (process.uid as number) > 0 && (process.gid as number) > 0;
}

function plain(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requestConversion(commandID: string, signal?: AbortSignal): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const body = JSON.stringify({ command_id: commandID });
    const request = httpRequest({
      socketPath, path: "/v1/prepare", method: "POST", signal,
      headers: { "content-type": "application/json", "content-length": Buffer.byteLength(body) },
    }, (response) => {
      const chunks: Buffer[] = [];
      let size = 0;
      response.on("data", (chunk: Buffer) => {
        size += chunk.length;
        if (size > 2 << 20) {
          request.destroy(new Error("materializer response exceeds size limit"));
          return;
        }
        chunks.push(chunk);
      });
      response.on("end", () => {
        if (response.statusCode !== 200) {
          reject(response.statusCode === 422
            ? new InvalidImageError("materializer rejected Docker image")
            : new Error(`materializer returned HTTP ${response.statusCode}`));
          return;
        }
        try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf8"))); }
        catch { reject(new Error("materializer returned invalid JSON")); }
      });
      response.on("error", reject);
    });
    request.setTimeout(45 * 60_000, () => request.destroy(new Error("materializer timed out")));
    request.on("error", reject);
    request.end(body);
  });
}
