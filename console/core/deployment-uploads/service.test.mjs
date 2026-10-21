import { expect, test } from "bun:test";

import { DeploymentService, publicRelease } from "./service";
import { observedRetainedBuilds } from "./retained-builds";
import { ControlError } from "../domain/errors";
import { edge, runtime } from "../api-deployments/protocol-artifact.test.mjs";

const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const uploadID = "upl_0123456789abcdef0123456789abcdef";
const releaseID = "rel_0123456789abcdef0123456789abcdef";
const apiDigest = `sha256:${"a".repeat(64)}`;
const sessionRuntimeDigest = `sha256:${"b".repeat(64)}`;
const at = new Date("2026-08-12T12:00:00Z");
const expiresAt = new Date("2026-08-12T13:00:00Z");
const manifest = {
  api: {
    digest: apiDigest,
    size_bytes: 27,
    manifest: edge,
  },
  session_runtime: {
    digest: sessionRuntimeDigest,
    size_bytes: 1024,
    media_type: "application/vnd.cantelop.session-runtime.image.v1.tar+gzip",
    manifest: runtime,
  },
};

function upload(status, overrides = {}) {
  return {
    id: uploadID,
    ownerUserID: userID,
    appID,
    idempotencyKey: "deploy-request-012345",
    status,
    input: {
      api: {
        digest: apiDigest,
        objectKey: "deployment-uploads/0123456789abcdef0123456789abcdef/api/worker.mjs",
        sizeBytes: 27,
        manifest: manifest.api.manifest,
      },
      sessionRuntime: {
        digest: sessionRuntimeDigest,
        objectKey: "deployment-uploads/0123456789abcdef0123456789abcdef/session-runtime/image.tar.gz",
        sizeBytes: 1024,
        mediaType: manifest.session_runtime.media_type,
        manifest: runtime,
      },
    },
    expiresAt,
    createdAt: at,
    updatedAt: at,
    ...overrides,
  };
}

function release() {
  return {
    id: releaseID,
    appID,
    version: 1,
    status: "queued",
    apiArtifactDigest: apiDigest,
    sessionRuntimeArtifactDigest: sessionRuntimeDigest,
    components: {
      api: { kind: "api", operationID: "op_0123456789abcdef0123456789abcdef", status: "queued" },
      session_runtime: { kind: "session_runtime", operationID: "op_fedcba9876543210fedcba9876543210", status: "queued" },
    },
    createdAt: at,
    updatedAt: at,
  };
}

test("returns direct API and multipart sessionRuntime upload targets", async () => {
  let prepared;
  const uploads = {
    async begin(_candidateID, owner, app, input, key) {
      expect({ owner, app, key }).toEqual({ owner: userID, app: appID, key: "deploy-request-012345" });
      return upload("preparing", { id: input.api.objectKey.split("/")[1].replace(/^/, "upl_") });
    },
    async markReady(_owner, _app, id, storeUploadID, partSizeBytes, partCount) {
      prepared = { id, storeUploadID, partSizeBytes, partCount };
      return upload("uploading", { id, storeUploadID, sessionRuntimePartSizeBytes: partSizeBytes, sessionRuntimePartCount: partCount });
    },
  };
  const artifacts = {
    async createSessionRuntimeUpload() { return { storeUploadID: "store-1", partSizeBytes: 64 << 20, partCount: 1 }; },
    async signAPIUpload() { return { url: "https://r2.test/api", headers: { "content-type": "application/javascript+module" } }; },
    async signSessionRuntimeParts() { return [{ partNumber: 1, url: "https://r2.test/session-runtime/1", headers: {} }]; },
  };
  const service = new DeploymentService(uploads, {}, artifacts, () => at);
  const result = await service.beginUpload(userID, appID, manifest, "deploy-request-012345");

  expect(prepared).toMatchObject({ storeUploadID: "store-1", partCount: 1 });
  expect(result).toMatchObject({
    app_id: appID,
    status: "uploading",
    api: { method: "PUT", url: "https://r2.test/api" },
    session_runtime: { part_size_bytes: 64 << 20, parts: [{ part_number: 1, method: "PUT" }] },
  });
});

test("completes uploaded parts and creates one durable release", async () => {
  const storeStates = new Map([["api", "matches"], ["session_runtime", "missing"]]);
  let completed = false;
  let finalized;
  const currentUpload = upload("uploading", {
    storeUploadID: "store-1",
    sessionRuntimePartSizeBytes: 64 << 20,
    sessionRuntimePartCount: 1,
  });
  const uploads = {
    async getForUser() { return currentUpload; },
  };
  const releases = {
    async begin(_user, _app, api, sessionRuntime, key, id) {
      finalized = { id, releaseID };
      expect({ api, sessionRuntime, key }).toEqual({
        api: currentUpload.input.api,
        sessionRuntime: currentUpload.input.sessionRuntime,
        key: currentUpload.idempotencyKey,
      });
      return release();
    },
  };
  const artifacts = {
    async inspectObject(key) { return storeStates.get(key.includes("/api/") ? "api" : "session_runtime"); },
    async completeSessionRuntimeUpload(_key, storeUploadID, parts, sizeBytes, partSizeBytes) {
      expect({ storeUploadID, parts, sizeBytes, partSizeBytes }).toEqual({
        storeUploadID: "store-1", parts: [{ partNumber: 1, etag: '"etag-1"' }],
        sizeBytes: 1024, partSizeBytes: 64 << 20,
      });
      completed = true;
      storeStates.set("session_runtime", "matches");
    },
  };
  const service = new DeploymentService(uploads, releases, artifacts, () => at);
  const result = await service.finalizeUpload(userID, appID, uploadID, {
    session_runtime_parts: [{ part_number: 1, etag: '"etag-1"' }],
  });

  expect(completed).toBe(true);
  expect(finalized).toEqual({ id: uploadID, releaseID });
  expect(result).toMatchObject({
    id: releaseID,
    app_id: appID,
    status: "queued",
    components: { api: { status: "queued" }, session_runtime: { status: "queued" } },
  });
});

test("deletes a mismatched Session runtime object immediately after completion", async () => {
  let completed = false;
  let deleted = false;
  const currentUpload = upload("uploading", {
    storeUploadID: "store-1", sessionRuntimePartSizeBytes: 64 << 20, sessionRuntimePartCount: 1,
  });
  const service = new DeploymentService({ async getForUser() { return currentUpload; } }, {
    async begin() { throw new Error("rejected object reached release creation"); },
  }, {
    async inspectObject(key) {
      return key.includes("/api/") ? "matches" : completed ? "mismatch" : "missing";
    },
    async completeSessionRuntimeUpload() { completed = true; },
    async deleteSessionRuntimeObject(key) {
      expect(key).toBe(currentUpload.input.sessionRuntime.objectKey);
      deleted = true;
    },
  }, () => at);
  const input = { session_runtime_parts: [{ part_number: 1, etag: '"etag-1"' }] };
  await expect(service.finalizeUpload(userID, appID, uploadID, input)).rejects.toMatchObject({ code: "conflict" });
  expect(completed).toBe(true);
  expect(deleted).toBe(true);
});

test("retries deletion of a mismatched Session runtime object on finalize", async () => {
  const currentUpload = upload("uploading", {
    storeUploadID: "store-1", sessionRuntimePartSizeBytes: 64 << 20, sessionRuntimePartCount: 1,
  });
  let deleteAttempts = 0;
  let deleted = false;
  const service = new DeploymentService({ async getForUser() { return currentUpload; } }, {
    async begin() { throw new Error("rejected object reached release creation"); },
  }, {
    async inspectObject(key) { return key.includes("/api/") ? "matches" : deleted ? "missing" : "mismatch"; },
    async deleteSessionRuntimeObject() {
      if (++deleteAttempts === 1) throw new Error("storage offline");
      deleted = true;
    },
    // The multipart upload was already completed, so the store reports a conflict.
    async completeSessionRuntimeUpload() { throw new ControlError("conflict"); },
  }, () => at);
  const input = { session_runtime_parts: [{ part_number: 1, etag: '"etag-1"' }] };
  await expect(service.finalizeUpload(userID, appID, uploadID, input)).rejects.toThrow("storage offline");
  await expect(service.finalizeUpload(userID, appID, uploadID, input)).rejects.toMatchObject({ code: "conflict" });
  await expect(service.finalizeUpload(userID, appID, uploadID, input)).rejects.toMatchObject({ code: "conflict" });
  expect(deleteAttempts).toBe(2);
});

test("lists pending uploads without signed URLs or storage identifiers", async () => {
  const service = new DeploymentService({
    async listPendingForUser(owner, app) {
      expect([owner, app]).toEqual([userID, appID]);
      return [upload("preparing")];
    },
  }, {}, {}, () => at);
  expect(await service.listUploads(userID, appID)).toEqual({ items: [{
    id: uploadID, app_id: appID, status: "preparing", expires_at: expiresAt.toISOString(),
  }] });
});

test("uses the App owner's tenant key for an admin's deployment reads", async () => {
  const ownerID = "usr_fedcba9876543210fedcba9876543210";
  const service = new DeploymentService({
    async listPendingForUser(owner, app) {
      expect([owner, app]).toEqual([ownerID, appID]);
      return [];
    },
  }, {}, {}, () => at, async (actor, app) => {
    expect([actor, app]).toEqual([userID, appID]);
    return ownerID;
  });
  expect(await service.listUploads(userID, appID)).toEqual({ items: [] });
});

test("cancels before storage cleanup and reports cleanup failures without relocking", async () => {
  let cancelled = false;
  let attempts = 0;
  const service = new DeploymentService({
    async cancelForUser(owner, app, id) {
      expect([owner, app, id]).toEqual([userID, appID, uploadID]);
      cancelled = true;
      return upload("cancelled", { storeUploadID: "store-1" });
    },
  }, {}, {
    async abortSessionRuntimeUpload(key, id) {
      expect(cancelled).toBe(true);
      expect([key, id]).toEqual([upload("uploading").input.sessionRuntime.objectKey, "store-1"]);
      if (++attempts === 1) throw new Error("storage offline");
    },
    async deleteSessionRuntimeObject(key) {
      expect(key).toBe(upload("uploading").input.sessionRuntime.objectKey);
    },
  }, () => at);
  expect(await service.cancelUpload(userID, appID, uploadID)).toMatchObject({
    id: uploadID, status: "cancelled", cleanup_pending: true, idempotency_key: "deploy-request-012345",
  });
  expect(await service.cancelUpload(userID, appID, uploadID)).toMatchObject({ status: "cancelled", cleanup_pending: false });
});

test("cancels preparing uploads without a multipart handle", async () => {
  const service = new DeploymentService({ async cancelForUser() { return upload("cancelled"); } }, {}, {}, () => at);
  expect(await service.cancelUpload(userID, appID, uploadID)).toMatchObject({ status: "cancelled", cleanup_pending: false });
});

test("does not abort storage when cancellation is rejected", async () => {
  const service = new DeploymentService({ async cancelForUser() { throw new Error("already finalized"); } }, {}, {}, () => at);
  await expect(service.cancelUpload(userID, appID, uploadID)).rejects.toThrow("already finalized");
});

test("cancelled uploads cannot be replayed or completed", async () => {
  const service = new DeploymentService({
    async begin() { return upload("cancelled"); },
    async getForUser() { return upload("cancelled"); },
  }, {}, {}, () => at);
  await expect(service.beginUpload(userID, appID, manifest, "deploy-request-012345")).rejects.toMatchObject({ code: "conflict" });
  await expect(service.finalizeUpload(userID, appID, uploadID, {
    session_runtime_parts: [{ part_number: 1, etag: '"etag-1"' }],
  })).rejects.toMatchObject({ code: "conflict" });
});

test("cancellation during multipart preparation aborts the newly created handle", async () => {
  let aborted = false;
  const { ControlError } = await import("../domain/errors");
  const service = new DeploymentService({
    async begin() { return upload("preparing"); },
    async markReady() { throw new ControlError("conflict"); },
    async getForUser() { return upload("cancelled"); },
  }, {}, {
    async createSessionRuntimeUpload() { return { storeUploadID: "late-store", partSizeBytes: 64 << 20, partCount: 1 }; },
    async abortSessionRuntimeUpload(_key, id) { expect(id).toBe("late-store"); aborted = true; },
  }, () => at);
  await expect(service.beginUpload(userID, appID, manifest, "deploy-request-012345")).rejects.toMatchObject({ code: "conflict" });
  expect(aborted).toBe(true);
});

test("publishes deployment activity without internal operation or provider output", () => {
  const document = release();
  document.components.api = {
    ...document.components.api, attempts: 3, updatedAt: at, nextAttemptAt: expiresAt,
    output: { private_provider_detail: "hidden" },
  };
  expect(JSON.parse(JSON.stringify(publicRelease(document))).components.api).toEqual({
    status: "queued", attempts: 3, updated_at: at.toISOString(), next_attempt_at: expiresAt.toISOString(),
  });
});

test("rejects pre-1.0 bundles before reserving anything", async () => {
  const service = new DeploymentService({}, {}, {});
  const { manifest: _runtime, ...withoutRuntime } = manifest.session_runtime;
  for (const legacy of [
    { ...manifest, api: { ...manifest.api, manifest: { schema_version: 2, kind: "cantelop-edge-api", main_module: "worker.mjs" } }, session_runtime: withoutRuntime },
    { ...manifest, session_runtime: withoutRuntime },
  ]) {
    await expect(service.beginUpload(userID, appID, legacy, "deploy-request-012345")).rejects.toThrow("invalid deployment bundle manifest");
  }
});

function rollbackHarness(retained, target = { id: releaseID, components: { session_runtime: { status: "succeeded", output: { build_id: `sha256:${"c".repeat(64)}` } } } }) {
  const calls = [];
  const releases = {
    async rollbackTargetForUser(owner, app) { calls.push(["target", owner, app]); return target; },
    async rollbackForUser(owner, app, expected) {
      calls.push(["rollback", owner, app, expected]);
      return { ...target, appID, version: 3, status: "superseded", apiArtifactDigest: apiDigest, sessionRuntimeArtifactDigest: sessionRuntimeDigest,
        components: { api: { status: "succeeded" }, session_runtime: { status: "succeeded" } }, createdAt: at, updatedAt: at };
    },
  };
  const service = new DeploymentService({}, releases, {}, () => at, async (user) => user, undefined, async (app) => {
    calls.push(["retained", app]);
    return typeof retained === "function" ? retained(app) : retained;
  });
  return { service, calls };
}

test("rollback rejects a target whose Build the host no longer retains", async () => {
  const { service, calls } = rollbackHarness(new Set([`sha256:${"d".repeat(64)}`]));
  await expect(service.rollback(userID, appID, releaseID)).rejects.toEqual(new ControlError("target_build_missing"));
  expect(calls.map(([name]) => name)).toEqual(["target", "retained"]);
});

test("rollback activates exactly the checked target when its Build is retained", async () => {
  const { service, calls } = rollbackHarness(new Set([`sha256:${"c".repeat(64)}`]));
  await service.rollback(userID, appID);
  expect(calls.at(-1)).toEqual(["rollback", userID, appID, releaseID]);
});

test("an observer proxy 404 falls back to activation rather than rejecting rollback", async () => {
  const { service, calls } = rollbackHarness((app) => observedRetainedBuilds(app, {
    FIRE_FUSE_OBSERVER_URL: "https://observer.example.test", FIRE_FUSE_STATUS_TOKEN: "t".repeat(32),
  }, async () => new Response("<html><title>404 Not Found</title></html>", { status: 404 })));
  await service.rollback(userID, appID, releaseID);
  expect(calls.map(([name]) => name)).toEqual(["target", "retained", "rollback"]);
  expect(calls.at(-1)).toEqual(["rollback", userID, appID, releaseID]);
});

test("rollback proceeds when retention is unknown or the console names another target", async () => {
  const unknown = rollbackHarness(null);
  await unknown.service.rollback(userID, appID, releaseID);
  expect(unknown.calls.at(-1)).toEqual(["rollback", userID, appID, releaseID]);
  const moved = rollbackHarness(new Set());
  await moved.service.rollback(userID, appID, `rel_${"9".repeat(32)}`);
  // The repository reports the conflict; a stale target is not checked.
  expect(moved.calls.map(([name]) => name)).toEqual(["target", "rollback"]);
});
