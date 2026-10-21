import { expect, test } from "bun:test";
import { createHash } from "node:crypto";

import { APIDeploymentWorker as Worker } from "./worker";

class APIDeploymentWorker extends Worker {
 constructor(id, operations, releases, ...rest) {
  super(id, { async recordProgress() {}, async getForUser() { return { status: "running", lockedBy: id }; }, ...operations },
   { async withDeploymentLock(_app, fn) { return fn(); }, ...releases }, rest[0], rest[1],
   { async projection() { return { revision: 1, values: [] }; } },
   { async replaceConfiguration() {} }, rest[2]);
 }
}

const module = Buffer.from("export default { fetch() {} }");
const hex = createHash("sha256").update(module).digest("hex");
const operation = {
  id: "op_0123456789abcdef0123456789abcdef",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "deploy_api",
  status: "running",
  idempotencyKey: "request-0123456789",
  input: {
    release_id: "rel_0123456789abcdef0123456789abcdef",
    artifact_id: "art_0123456789abcdef0123456789abcdef",
    object_key: "deployment-uploads/0123456789abcdef0123456789abcdef/api/worker.mjs",
    artifact_digest: `sha256:${hex}`,
  },
  attempts: 1,
};

test("deploys a claimed release artifact and completes its component", async () => {
  const calls = [];
  const worker = new APIDeploymentWorker(
    "api-worker-1",
    { async claim(id, kind) { calls.push(["claim", id, kind]); return operation; } },
    {
      async startComponent(id, workerID) { calls.push(["start", id, workerID]); },
      async succeedComponent(id, workerID, output) { calls.push(["succeed", id, workerID, output]); },
    },
    { async get(key) { calls.push(["get", key]); return module; } },
    { async deploy(app, release, body) { calls.push(["deploy", app, release, body]); return { id: "release-1", etag: "etag-1", scriptName: operation.input.release_id }; } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(calls.map(([name]) => name)).toEqual(["claim", "start", "get", "deploy", "succeed"]);
  expect(calls.at(-1)).toEqual([
    "succeed",
    operation.id,
    "api-worker-1",
    { provider_release_id: "release-1", provider_etag: "etag-1", script_name: operation.input.release_id },
  ]);
});

test("retries a transient provider failure through the release aggregate", async () => {
  let retried;
  let failed = false;
  const worker = new APIDeploymentWorker(
    "api-worker-1",
    { async claim() { return operation; } },
    {
      async startComponent() {},
      async retryComponent(id, workerID, availableAt) { retried = { id, workerID, availableAt }; },
      async failComponent() { failed = true; },
    },
    { async get() { return module; } },
    { async deploy() { throw new Error("provider unavailable"); } },
    () => new Date("2026-08-12T12:00:00Z"),
  );
  expect(await worker.runOnce()).toBe(true);
  expect(retried).toMatchObject({ id: operation.id, workerID: "api-worker-1" });
  expect(retried.availableAt.getTime()).toBeGreaterThan(new Date("2026-08-12T12:00:00Z").getTime());
  expect(failed).toBe(false);
});

test("fails corrupted release artifacts without calling the provider", async () => {
  let errorCode;
  let deployed = false;
  const worker = new APIDeploymentWorker(
    "api-worker-1",
    { async claim() { return operation; } },
    {
      async startComponent() {},
      async failComponent(_id, _worker, code) { errorCode = code; },
    },
    { async get() { return Buffer.from("corrupted"); } },
    { async deploy() { deployed = true; return {}; } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(errorCode).toBe("artifact_integrity_failed");
  expect(deployed).toBe(false);
});

test("rejects the removed API-only operation shape", async () => {
  let errorCode;
  const worker = new APIDeploymentWorker(
    "api-worker-1",
    { async claim() { return { ...operation, input: {
      deployment_id: "adep_0123456789abcdef0123456789abcdef",
      artifact_id: operation.input.artifact_id,
      object_key: `api-artifacts/${hex}/worker.mjs`,
      artifact_digest: operation.input.artifact_digest,
    } }; }, async fail(_id, _worker, code) { errorCode = code; } },
    {},
    { async get() { throw new Error("must not read"); } },
    { async deploy() { throw new Error("must not deploy"); } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(errorCode).toBe("invalid_operation_input");
});

test("does not call the provider when cancellation wins before the deployment lock", async () => {
 let deployed=false;
 const worker=new APIDeploymentWorker("api-worker",{
  async claim(){return operation;},async getForUser(){return {status:"failed",errorCode:"deployment_cancelled"};},
 },{}, {async get(){return module;}},{async deploy(){deployed=true;}});
 expect(await worker.runOnce()).toBe(true);
 expect(deployed).toBe(false);
});

test("late provider completion after cancellation does not crash the worker", async () => {
 let reads=0;
 const worker=new APIDeploymentWorker("api-worker",{
  async claim(){return operation;},async getForUser(){return ++reads===1?{status:"running",lockedBy:"api-worker"}:{status:"failed"};},
 },{async startComponent(){},async succeedComponent(){throw Error("lost ownership");},async retryComponent(){throw Error("lost ownership");}},
 {async get(){return module;}},{async deploy(){return {id:"late"};}});
 expect(await worker.runOnce()).toBe(true);
});

test("persists safe provider HTTP diagnostics before retrying", async () => {
  const { APIProviderError } = await import("./provider");
  const progress = [];
  const worker = new APIDeploymentWorker("api-worker-1", {
    async claim() { return operation; },
    async recordProgress(_id, _worker, code) { progress.push(code); },
  }, { async startComponent() {}, async retryComponent() {} },
  { async get() { return module; } },
  { async deploy() { throw new APIProviderError("private provider detail", "api_provider_http_503"); } });
  await worker.runOnce();
  expect(progress).toEqual(["artifact_verifying", "api_deploying", "api_provider_http_503"]);
});
