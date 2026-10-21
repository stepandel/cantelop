import { expect, test } from "bun:test";
import { S3Client } from "@aws-sdk/client-s3";

import { configuredDeploymentArtifactStore, S3DeploymentArtifactStore } from "./s3-artifact-store";

const suffix = "0123456789abcdef0123456789abcdef";
const apiKey = `deployment-uploads/${suffix}/api/worker.mjs`;
const sessionRuntimeKey = `deployment-uploads/${suffix}/session-runtime/image.tar.gz`;
const apiDigest = `sha256:${"a".repeat(64)}`;
const sessionRuntimeDigest = `sha256:${"b".repeat(64)}`;

test("multipart abort is idempotent but does not hide storage outages", async () => {
  let error = Object.assign(new Error("gone"), { name: "NoSuchUpload" });
  const store = new S3DeploymentArtifactStore({ async send(command) {
    expect(command.constructor.name).toBe("AbortMultipartUploadCommand");
    expect(command.input).toEqual({ Bucket: "cantelop-artifacts", Key: sessionRuntimeKey, UploadId: "store-1" });
    throw error;
  } }, "cantelop-artifacts");
  await store.abortSessionRuntimeUpload(sessionRuntimeKey, "store-1");
  error = new Error("storage offline");
  await expect(store.abortSessionRuntimeUpload(sessionRuntimeKey, "store-1")).rejects.toThrow("storage offline");
});

test("creates bounded multipart targets without exposing store credentials", async () => {
  const commands = [];
  const signed = [];
  const client = {
    async send(command) {
      commands.push(command);
      if (command.constructor.name === "CreateMultipartUploadCommand") return { UploadId: "r2-upload-1" };
      return {};
    },
  };
  const store = new S3DeploymentArtifactStore(client, "cantelop-artifacts", async (_client, command, expires) => {
    signed.push({ command, expires });
    return `https://uploads.example.test/${signed.length}`;
  });
  const multipart = await store.createSessionRuntimeUpload(sessionRuntimeKey, 65 << 20, sessionRuntimeDigest);
  expect(multipart).toEqual({ storeUploadID: "r2-upload-1", partSizeBytes: 64 << 20, partCount: 2 });
  const api = await store.signAPIUpload(apiKey, 27, apiDigest, 900);
  const parts = await store.signSessionRuntimeParts(sessionRuntimeKey, multipart, 65 << 20, 900);

  expect(api).toEqual({
    url: "https://uploads.example.test/1",
    headers: {
      "content-type": "application/javascript+module",
      "x-amz-checksum-sha256": Buffer.from("a".repeat(64), "hex").toString("base64"),
      "x-amz-meta-cantelop-sha256": "a".repeat(64),
    },
  });
  expect(parts.map((part) => part.partNumber)).toEqual([1, 2]);
  expect(signed.slice(1).map(({ command }) => command.input.ContentLength)).toEqual([64 << 20, 1 << 20]);
  expect(signed.every(({ expires }) => expires === 900)).toBe(true);
  expect(JSON.stringify({ api, parts })).not.toContain("r2-upload-1");
  expect(commands[0].input).toMatchObject({
    Key: sessionRuntimeKey,
    Metadata: { "cantelop-sha256": "b".repeat(64) },
  });
  expect(await store.signSessionRuntimeDownload(sessionRuntimeKey, 900)).toBe("https://uploads.example.test/4");
});

test("keeps API integrity metadata in signed request headers", async () => {
  const client = new S3Client({
    endpoint: "https://example.r2.cloudflarestorage.com",
    region: "auto",
    credentials: { accessKeyId: "test-access-key", secretAccessKey: "test-secret-key" },
  });
  const store = new S3DeploymentArtifactStore(client, "cantelop-artifacts");

  const target = await store.signAPIUpload(apiKey, 27, apiDigest, 900);
  const url = new URL(target.url);

  expect(url.searchParams.has("x-amz-checksum-sha256")).toBe(false);
  expect(url.searchParams.has("x-amz-meta-cantelop-sha256")).toBe(false);
  expect(url.searchParams.get("X-Amz-SignedHeaders")).toContain("x-amz-checksum-sha256");
  expect(url.searchParams.get("X-Amz-SignedHeaders")).toContain("x-amz-meta-cantelop-sha256");
  expect(target.headers["x-amz-meta-cantelop-sha256"]).toBe("a".repeat(64));

  const parts = await store.signSessionRuntimeParts(sessionRuntimeKey, {
    storeUploadID: "r2-upload-1", partSizeBytes: 64 << 20, partCount: 2,
  }, 65 << 20, 900);
  expect(parts.map(({ url }) => new URL(url).searchParams.get("X-Amz-SignedHeaders")))
    .toEqual(["content-length;host", "content-length;host"]);
});

test("keeps signed artifact URLs on the configured store origin", async () => {
  const endpoint = "https://account-id.r2.cloudflarestorage.com";
  const store = configuredDeploymentArtifactStore({
    CANTELOP_ARTIFACT_BUCKET: "cantelop-artifacts",
    CANTELOP_ARTIFACT_ENDPOINT: endpoint,
    CANTELOP_ARTIFACT_ACCESS_KEY_ID: "test-access-key",
    CANTELOP_ARTIFACT_SECRET_ACCESS_KEY: "test-secret-key",
  });

  const url = new URL(await store.signSessionRuntimeDownload(sessionRuntimeKey, 900));

  expect(url.origin).toBe(endpoint);
  expect(url.pathname).toBe(`/cantelop-artifacts/${sessionRuntimeKey}`);
});

test("completes ordered parts and verifies staged object metadata", async () => {
  const commands = [];
  const client = {
    async send(command) {
      commands.push(command);
      if (command.constructor.name === "ListPartsCommand") return { Parts: [
        { PartNumber: 1, ETag: '"part-1"', Size: 64 << 20 },
        { PartNumber: 2, ETag: '"part-2"', Size: 1 << 20 },
      ] };
      if (command.constructor.name === "HeadObjectCommand") {
        return { ContentLength: 1234, Metadata: { "cantelop-sha256": "b".repeat(64) } };
      }
      return {};
    },
  };
  const store = new S3DeploymentArtifactStore(client, "cantelop-artifacts", async () => "https://unused.example.test");
  await store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 1, etag: '"part-1"' },
    { partNumber: 2, etag: '"part-2"' },
  ], 65 << 20, 64 << 20);
  expect(await store.inspectObject(sessionRuntimeKey, 1234, sessionRuntimeDigest)).toBe("matches");
  expect(commands[1].input.MultipartUpload.Parts).toEqual([
    { PartNumber: 1, ETag: '"part-1"' },
    { PartNumber: 2, ETag: '"part-2"' },
  ]);
  await expect(store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 2, etag: '"part-2"' },
  ], 65 << 20, 64 << 20)).rejects.toThrow();
});

test("rejects an oversized staged part without aborting the upload", async () => {
  const commands = [];
  const store = new S3DeploymentArtifactStore({ async send(command) {
    commands.push(command.constructor.name);
    if (command.constructor.name === "ListPartsCommand") return { Parts: [
      { PartNumber: 1, ETag: '"part-1"', Size: 128 << 20 },
      { PartNumber: 2, ETag: '"part-2"', Size: 1 << 20 },
    ] };
    return {};
  } }, "cantelop-artifacts");
  await expect(store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 1, etag: '"part-1"' },
    { partNumber: 2, etag: '"part-2"' },
  ], 65 << 20, 64 << 20)).rejects.toMatchObject({ code: "conflict" });
  expect(commands).toEqual(["ListPartsCommand"]);
});

test("rejects a replaced part even when its size is correct", async () => {
  const commands = [];
  const store = new S3DeploymentArtifactStore({ async send(command) {
    commands.push(command.constructor.name);
    if (command.constructor.name === "ListPartsCommand") return { Parts: [
      { PartNumber: 1, ETag: '"different"', Size: 1 << 20 },
    ] };
    return {};
  } }, "cantelop-artifacts");
  await expect(store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 1, etag: '"part-1"' },
  ], 1 << 20, 64 << 20)).rejects.toMatchObject({ code: "conflict" });
  expect(commands).toEqual(["ListPartsCommand"]);
});

test("accepts ETags that differ only by surrounding quotes", async () => {
  const commands = [];
  const store = new S3DeploymentArtifactStore({ async send(command) {
    commands.push(command.constructor.name);
    if (command.constructor.name === "ListPartsCommand") return { Parts: [{ PartNumber: 1, ETag: '"part-1"', Size: 1 << 20 }] };
    return {};
  } }, "cantelop-artifacts");
  await store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 1, etag: "part-1" },
  ], 1 << 20, 64 << 20);
  expect(commands).toEqual(["ListPartsCommand", "CompleteMultipartUploadCommand"]);
});

test("rejects a missing trailing part without aborting the upload", async () => {
  const commands = [];
  const store = new S3DeploymentArtifactStore({ async send(command) {
    commands.push(command.constructor.name);
    if (command.constructor.name === "ListPartsCommand") return { Parts: [{ PartNumber: 1, ETag: '"part-1"', Size: 64 << 20 }] };
    return {};
  } }, "cantelop-artifacts");
  await expect(store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 1, etag: '"part-1"' },
    { partNumber: 2, etag: '"part-2"' },
  ], 65 << 20, 64 << 20)).rejects.toMatchObject({ code: "conflict" });
  expect(commands).toEqual(["ListPartsCommand"]);
});

test("reports a finished multipart upload as a conflict", async () => {
  const store = new S3DeploymentArtifactStore({ async send() {
    throw Object.assign(new Error("gone"), { name: "NoSuchUpload" });
  } }, "cantelop-artifacts");
  await expect(store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", [
    { partNumber: 1, etag: '"part-1"' },
  ], 1 << 20, 64 << 20)).rejects.toMatchObject({ code: "conflict" });
});

test("pages through listed parts and rejects a non-advancing marker", async () => {
  const markers = [];
  const pages = [
    { Parts: [{ PartNumber: 1, ETag: '"part-1"', Size: 64 << 20 }], IsTruncated: true, NextPartNumberMarker: "1" },
    { Parts: [{ PartNumber: 2, ETag: '"part-2"', Size: 1 << 20 }], IsTruncated: false },
  ];
  const store = new S3DeploymentArtifactStore({ async send(command) {
    if (command.constructor.name !== "ListPartsCommand") return {};
    markers.push(command.input.PartNumberMarker);
    return pages[markers.length - 1];
  } }, "cantelop-artifacts");
  const parts = [{ partNumber: 1, etag: '"part-1"' }, { partNumber: 2, etag: '"part-2"' }];
  await store.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", parts, 65 << 20, 64 << 20);
  expect(markers).toEqual([undefined, "1"]);

  const stuck = new S3DeploymentArtifactStore({ async send(command) {
    if (command.constructor.name !== "ListPartsCommand") return {};
    return { Parts: [], IsTruncated: true, NextPartNumberMarker: command.input.PartNumberMarker ?? "1" };
  } }, "cantelop-artifacts");
  await expect(stuck.completeSessionRuntimeUpload(sessionRuntimeKey, "r2-upload-1", parts, 65 << 20, 64 << 20))
    .rejects.toThrow("invalid part listing");
});
