import { expect, test } from "bun:test";

import { ComputeRegistrationProviderError } from "./provider";
import { ComputeRegistrationWorker } from "./worker";

const operation = {
  id: "op_0123456789abcdef0123456789abcdef",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "register_app",
  status: "running",
  idempotencyKey: "register-app:app_0123456789abcdef0123456789abcdef",
  input: {
    app_slug: "agent",
    internal_port: 3000,
  },
  attempts: 1,
};

test("registers a claimed App and completes the durable operation", async () => {
  const calls = [];
  const worker = new ComputeRegistrationWorker(
    "control-worker-1",
    {
      async claim(id, kind) { calls.push(["claim", id, kind]); return operation; },
      async succeed(id, workerID, output) { calls.push(["succeed", id, workerID, output]); },
    },
    { async registerApp(command) { calls.push(["register", command]); } },
    { async publish(command) { calls.push(["publish", command]); } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["claim", "control-worker-1", "register_app"],
    ["register", {
      ownerUserID: operation.ownerUserID,
      appID: operation.appID,
      appSlug: "agent",
      internalPort: 3000,
    }],
    ["publish", { appID: operation.appID, appSlug: "agent" }],
    ["succeed", operation.id, "control-worker-1", { provider_app_id: operation.appID }],
  ]);
});

test("retries transient registration failures", async () => {
  let retried;
  const worker = new ComputeRegistrationWorker(
    "control-worker-1",
    {
      async claim() { return operation; },
      async retry(id, workerID, availableAt) { retried = { id, workerID, availableAt }; },
    },
    { async registerApp() { throw new ComputeRegistrationProviderError(true); } },
    { async publish() {} },
    () => new Date("2026-08-12T12:00:00Z"),
  );
  expect(await worker.runOnce()).toBe(true);
  expect(retried.availableAt).toEqual(new Date("2026-08-12T12:00:30Z"));
});
