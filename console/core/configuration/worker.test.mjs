import { expect, test } from "bun:test";

import { ConfigurationProjectionWorker } from "./worker";

const operation = {
  id: "op_0123456789abcdef0123456789abcdef",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "sync_config",
  status: "running",
  idempotencyKey: "sync-config:app_0123456789abcdef0123456789abcdef:2",
  input: { revision: 2 },
  attempts: 1,
};

test("projects one revision to API and compute before completing", async () => {
  const calls = [];
  const projection = { revision: 2, values: [{ name: "TOKEN", kind: "secret", value: "secret" }] };
  const worker = new ConfigurationProjectionWorker(
    "control-worker-1",
    {
      async claim(id, kind) { calls.push(["claim", id, kind]); return operation; },
      async recordEdgeConfiguration(id, workerID, revision) { calls.push(["edge", id, workerID, revision]); },
      async succeed(id, workerID, output) { calls.push(["succeed", id, workerID, output]); },
    },
    {
      async projection() { calls.push(["read"]); return projection; },
      async revision() { calls.push(["revision"]); return 2; },
    },
    { async replaceConfiguration(value) { calls.push(["api", value]); } },
    { async replaceConfiguration(value) { calls.push(["compute", value]); } },
    { async withDeploymentLock(_app, action) { return action(); }, async activeScript() { return operation.appID; } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(calls.map(([name]) => name)).toEqual(["claim", "read", "api", "edge", "compute", "revision", "succeed"]);
  expect(calls.at(-1)).toEqual(["succeed", operation.id, "control-worker-1", { revision: 2 }]);
});

test("does not project an operation superseded before claim", async () => {
  let projected = false;
  let failed;
  const worker = new ConfigurationProjectionWorker(
    "control-worker-1",
    {
      async claim() { return operation; },
      async fail(_id, _worker, code) { failed = code; },
    },
    { async projection() { return { revision: 3, values: [] }; }, async revision() { return 3; } },
    { async replaceConfiguration() { projected = true; } },
    { async replaceConfiguration() { projected = true; } },
    { async withDeploymentLock(_app, action) { return action(); }, async activeScript() { return operation.appID; } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(projected).toBe(false);
  expect(failed).toBe("config_superseded");
});


test("records Edge propagation even when compute subsequently fails", async () => {
  const { ComputeRegistrationProviderError } = await import("../compute-registrations/provider");
  const calls = [];
  const worker = new ConfigurationProjectionWorker("control-worker-1", {
    async claim() { return operation; },
    async recordEdgeConfiguration(_id, _worker, revision) { calls.push(["edge", revision]); },
    async fail(_id, _worker, code) { calls.push(["failed", code]); },
  }, {
    async projection() { return { revision: 2, values: [] }; },
    async revision() { return 2; },
  }, { async replaceConfiguration() { calls.push(["api"]); } }, {
    async replaceConfiguration() { throw new ComputeRegistrationProviderError(false); },
  }, { async withDeploymentLock(_app, action) { return action(); }, async activeScript() { return operation.appID; } });
  await worker.runOnce();
  expect(calls).toEqual([["api"], ["edge", 2], ["failed", "provider_configuration_rejected"]]);
});

test("does not record propagation when Edge rejects configuration", async () => {
  const { ConfigurationProviderError } = await import("./provider");
  let recorded = false;
  let failed;
  const worker = new ConfigurationProjectionWorker("control-worker-1", {
    async claim() { return operation; },
    async recordEdgeConfiguration() { recorded = true; },
    async fail(_id, _worker, code) { failed = code; },
  }, {
    async projection() { return { revision: 2, values: [] }; },
    async revision() { return 2; },
  }, { async replaceConfiguration() { throw new ConfigurationProviderError(false); } }, {
    async replaceConfiguration() { throw new Error("unexpected compute call"); },
  }, { async withDeploymentLock(_app, action) { return action(); }, async activeScript() { return operation.appID; } });
  await worker.runOnce();
  expect(recorded).toBe(false);
  expect(failed).toBe("provider_configuration_rejected");
});
