import { expect, test } from "bun:test";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { S3PreparedArtifactStore } from "./prepared-artifact-store";

test("uploads the exact ext4 bytes and publishes the manifest last", async () => {
  const root = await mkdtemp(join(tmpdir(), "prepared-artifact-test-"));
  try {
    const imagePath = join(root, "rootfs.ext4");
    const image = Buffer.from("small ext4 fixture");
    await writeFile(imagePath, image);
    const digest = createHash("sha256").update(image).digest("hex");
    const commands = [];
    const client = { async send(command) {
      const name = command.constructor.name;
      commands.push({ name, input: command.input });
      if (name === "HeadObjectCommand") {
        const prior = commands.filter((item) => item.name === "HeadObjectCommand" && item.input.Key === command.input.Key);
        if (prior.length === 1) throw { name: "NotFound" };
        return { ContentLength: command.input.Key.endsWith(".ext4") ? image.length : Buffer.byteLength(JSON.stringify(manifest)),
          Metadata: { "cantelop-sha256": command.input.Key.endsWith(".ext4") ? digest :
            createHash("sha256").update(JSON.stringify(manifest)).digest("hex") } };
      }
      if (name === "CreateMultipartUploadCommand") return { UploadId: "upload-id" };
      if (name === "UploadPartCommand") return { ETag: '"part-etag"' };
      return {};
    } };
    const store = new S3PreparedArtifactStore(client, "cantelop-artifacts");
    const manifest = { schema_version: 1, build_id: `sha256:${"b".repeat(64)}`,
      image_key: `prepared-images/sha256/${digest}.ext4`, rootfs_sha256: digest,
      rootfs_size_bytes: image.length,
      process: { args: ["/app"], env: [], working_directory: "/", uid: 10001, gid: 10001 } };
    expect(await store.uploadImage(imagePath, digest, image.length)).toBe(manifest.image_key);
    expect(await store.uploadManifest(manifest)).toMatchObject({ key: expect.stringMatching(/^prepared-images\/manifests\/[0-9a-f]{64}\.json$/) });
    expect(commands.map(({ name }) => name)).toEqual([
      "HeadObjectCommand", "CreateMultipartUploadCommand", "UploadPartCommand", "CompleteMultipartUploadCommand", "HeadObjectCommand",
      "HeadObjectCommand", "PutObjectCommand", "HeadObjectCommand",
    ]);
    expect(Buffer.from(commands[2].input.Body)).toEqual(image);
    expect(commands[6].input.IfNoneMatch).toBe("*");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("refuses an existing image with conflicting digest metadata", async () => {
  const root = await mkdtemp(join(tmpdir(), "prepared-artifact-conflict-"));
  try {
    const path = join(root, "rootfs.ext4");
    await writeFile(path, "image");
    const store = new S3PreparedArtifactStore({ async send() {
      return { ContentLength: 5, Metadata: { "cantelop-sha256": "f".repeat(64) } };
    } }, "cantelop-artifacts");
    await expect(store.uploadImage(path, "a".repeat(64), 5)).rejects.toThrow("conflicting prepared artifact");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("reads a retained manifest only when its address matches its bytes", async () => {
  const manifest = { schema_version: 1, build_id: `sha256:${"b".repeat(64)}`,
    image_key: `prepared-images/sha256/${"a".repeat(64)}.ext4`, rootfs_sha256: "a".repeat(64),
    rootfs_size_bytes: 1024,
    process: { args: ["/app"], env: [], working_directory: "/", uid: 10001, gid: 10001 } };
  const bytes = Buffer.from(JSON.stringify(manifest));
  const digest = createHash("sha256").update(bytes).digest("hex");
  const store = new S3PreparedArtifactStore({ async send() {
    return { ContentLength: bytes.length, Body: { transformToWebStream: () => new ReadableStream({
      start(controller) { controller.enqueue(bytes); controller.close(); },
    }) } };
  } }, "cantelop-artifacts");
  expect(await store.readManifest(`prepared-images/manifests/${digest}.json`, `sha256:${digest}`)).toEqual(manifest);
  await expect(store.readManifest(`prepared-images/manifests/${"f".repeat(64)}.json`, `sha256:${"f".repeat(64)}`))
    .rejects.toThrow("checksum mismatch");
});
