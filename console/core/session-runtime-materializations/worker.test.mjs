import { expect, test } from "bun:test";
import { SessionRuntimeMaterializationWorker } from "./worker";
import { SessionRuntimeProviderError } from "./provider";
import { InvalidImageError } from "./local-builder";

const operation = {
  id: "op_0123456789abcdef0123456789abcdef",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "materialize_build", status: "running", lockedBy: "worker", attempts: 1,
  input: {
    release_id: "rel_0123456789abcdef0123456789abcdef",
    artifact_id: "sra_0123456789abcdef0123456789abcdef",
    object_key: "deployment-uploads/0123456789abcdef0123456789abcdef/session-runtime/image.tar.gz",
    artifact_digest: `sha256:${"b".repeat(64)}`, size_bytes: 4096,
  },
};

const manifest = {
  schema_version: 1, build_id: `sha256:${"c".repeat(64)}`,
  image_key: `prepared-images/sha256/${"d".repeat(64)}.ext4`,
  rootfs_sha256: "d".repeat(64), rootfs_size_bytes: 8192,
  process: { args: ["/app"], env: [], working_directory: "/", uid: 10001, gid: 10001 },
};

function fixtures(calls, provider, builder) {
  return new SessionRuntimeMaterializationWorker(
    "worker",
    {
      async claim() { calls.push("claim"); return operation; },
      async getForUser() { return operation; },
      async recordProgress(_id, _worker, progress) { calls.push(progress); },
    },
    {
      async startComponent() { calls.push("start"); },
      async succeedComponent(_id, _worker, output) { calls.push(output); },
      async failComponent(_id, _worker, code) { calls.push(`fail:${code}`); },
      async retryComponent() { calls.push("retry"); },
    },
    { async signSessionRuntimeDownload() { calls.push("sign-source"); return "https://r2.test/archive?signed=1"; } },
    builder ?? {
      async prepare() { calls.push("prepare"); return { manifest, imagePath: "/var/lib/cantelop-builder/images/image.ext4" }; },
      async removeImage() { calls.push("remove-image"); },
    },
    {
      async uploadImage() { calls.push("upload-image"); return manifest.image_key; },
      async uploadManifest() { calls.push("upload-manifest"); return { key: "prepared-images/manifests/manifest.json", digest: `sha256:${"e".repeat(64)}` }; },
      async signImageDownload() { calls.push("sign-image"); return "https://r2.test/image?signed=1"; },
    },
    provider,
  );
}

test("uploads the prepared image before installing and records its durable manifest", async () => {
  const calls = [];
  const worker = fixtures(calls, { async materialize(command) {
    calls.push(command);
    return { operationID: "bop_0123456789abcdef0123456789abcdef", status: "succeeded", buildID: manifest.build_id, buildVersion: 4 };
  } });
  expect(await worker.runOnce()).toBe(true);
  expect(calls.slice(0, 7)).toEqual(["claim", "start", "sign-source", "prepare", "upload-image", "upload-manifest", "sign-image"]);
  expect(calls[7].manifest).toEqual(manifest);
  expect(calls[7].imageURL).toBe("https://r2.test/image?signed=1");
  expect(calls[8]).toBe("remove-image");
  expect(calls.at(-1)).toEqual({
    provider_operation_id: "bop_0123456789abcdef0123456789abcdef",
    build_id: manifest.build_id, build_version: 4,
    prepared_manifest_key: "prepared-images/manifests/manifest.json",
    prepared_manifest_digest: `sha256:${"e".repeat(64)}`,
  });
});

test("retries a transient host response after publishing the durable artifact", async () => {
  const calls = [];
  const worker = fixtures(calls, { async materialize() { throw new SessionRuntimeProviderError(true); } });
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toContain("remove-image");
  expect(calls.at(-1)).toBe("retry");
});

test("fails a host retention quota without retrying", async () => {
  const calls = [];
  const worker = fixtures(calls, { async materialize() { throw new SessionRuntimeProviderError(false, "build_retention_quota"); } });
  expect(await worker.runOnce()).toBe(true);
  expect(calls.at(-1)).toBe("fail:build_retention_quota");
});

test("fails an invalid Docker image without repeated conversion", async () => {
  const calls = [];
  const worker = fixtures(calls, { async materialize() { throw new Error("host must not be contacted"); } },
    { async prepare() { throw new InvalidImageError("invalid Docker archive"); }, async removeImage() {} });
  expect(await worker.runOnce()).toBe(true);
  expect(calls.at(-1)).toBe("fail:invalid_session_image");
});
