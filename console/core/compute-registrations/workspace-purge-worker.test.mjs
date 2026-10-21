import { expect, test } from "bun:test";

import { ComputeWorkspacePurgeWorker } from "./workspace-purge-worker";

const operation = {
  id: "op_abcdef0123456789abcdef0123456789",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "purge_workspace",
  status: "running",
  idempotencyKey: "purge-workspace:wsp_abcdef0123456789abcdef0123456789",
  input: { workspace_id: "wsp_abcdef0123456789abcdef0123456789" },
  attempts: 1,
};

const command = {
  ownerUserID: operation.ownerUserID,
  appID: operation.appID,
  workspaceID: operation.input.workspace_id,
};

test("cancels registration before permanently purging its Workspace", async () => {
  const calls = [];
  let running = true;
  const worker = new ComputeWorkspacePurgeWorker(
    "control-worker-1",
    {
      async claim(workerID, kind) { calls.push(["claim", workerID, kind]); return operation; },
      async cancelQueuedWorkspaceRegistration(...args) { calls.push(["cancel", ...args]); },
      async hasRunningWorkspaceRegistration(...args) { calls.push(["running", ...args]); return running; },
      async retry(_id, _worker, at) { calls.push(["retry", at]); },
      async succeed(...args) { calls.push(["succeed", ...args]); },
    },
    {
      async archiveWorkspace(command) { calls.push(["archive", command]); },
      async purgeWorkspace(command) { calls.push(["purge", command]); },
    },
    { async markWorkspaceHostArchived(id) { calls.push(["mark", id]); } },
    () => new Date("2026-08-12T12:00:00Z"),
  );

  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["claim", "control-worker-1", "purge_workspace"],
    ["archive", command],
    ["cancel", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["running", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["retry", new Date("2026-08-12T12:00:30Z")],
  ]);

  calls.length = 0;
  running = false;
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["claim", "control-worker-1", "purge_workspace"],
    ["archive", command],
    ["cancel", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["running", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["archive", command],
    ["mark", command.workspaceID],
    ["purge", command],
    ["succeed", operation.id, "control-worker-1", { provider_workspace_id: operation.input.workspace_id }],
  ]);
});

test("purges even when the early host archive fails", async () => {
  const calls = [];
  const worker = new ComputeWorkspacePurgeWorker(
    "control-worker-1",
    {
      async claim() { return operation; },
      async cancelQueuedWorkspaceRegistration() {},
      async hasRunningWorkspaceRegistration() { return false; },
      async succeed(...args) { calls.push(["succeed", ...args]); },
    },
    {
      async archiveWorkspace() { calls.push(["archive"]); throw new Error("compute unavailable"); },
      async purgeWorkspace(command) { calls.push(["purge", command]); },
    },
    { async markWorkspaceHostArchived() { calls.push(["mark"]); } },
  );

  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["archive"],
    ["archive"],
    ["purge", command],
    ["mark"],
    ["succeed", operation.id, "control-worker-1", { provider_workspace_id: operation.input.workspace_id }],
  ]);
});


test("does not acknowledge absence while registration can restore the Workspace", async () => {
  let present = false;
  let marked = false;
  let running = true;
  let retries = 0;
  const worker = new ComputeWorkspacePurgeWorker("control-worker-1", {
    async claim() { return operation; },
    async cancelQueuedWorkspaceRegistration() {},
    async hasRunningWorkspaceRegistration() { return running; },
    async retry() { retries++; },
    async succeed() {},
  }, {
    async archiveWorkspace() { present = false; },
    async purgeWorkspace() { expect(present).toBe(false); },
  }, {
    async markWorkspaceHostArchived() { expect(running).toBe(false); expect(present).toBe(false); marked = true; },
  });
  await worker.runOnce();
  expect(retries).toBe(1);
  expect(marked).toBe(false);
  // The in-flight registration arrives after an absent-workspace archive.
  present = true;
  running = false;
  await worker.runOnce();
  expect(marked).toBe(true);
  expect(present).toBe(false);
});

test("archives again if registration completes between early archive and running check", async () => {
  let archives = 0;
  let present = false;
  let marked = false;
  const worker = new ComputeWorkspacePurgeWorker("control-worker-1", {
    async claim() { return operation; },
    async cancelQueuedWorkspaceRegistration() {},
    async hasRunningWorkspaceRegistration() { present = true; return false; },
    async succeed() {},
  }, {
    async archiveWorkspace() { archives++; present = false; },
    async purgeWorkspace() {},
  }, {
    async markWorkspaceHostArchived() { expect(present).toBe(false); marked = true; },
  });
  await worker.runOnce();
  expect(archives).toBe(2);
  expect(marked).toBe(true);
});

test("retries a failed final marker write after successful purge", async () => {
  let succeeded = false;
  let purges = 0;
  let retries = 0;
  let failMark = true;
  let marked = false;
  const worker = new ComputeWorkspacePurgeWorker("control-worker-1", {
    async claim() { return operation; },
    async cancelQueuedWorkspaceRegistration() {},
    async hasRunningWorkspaceRegistration() { return false; },
    async succeed() { succeeded = true; },
    async retry() { retries++; },
  }, {
    async archiveWorkspace() { throw new Error("transient archive failure"); },
    async purgeWorkspace() { purges++; },
  }, {
    async markWorkspaceHostArchived() { if (failMark) throw new Error("database unavailable"); marked = true; },
  });
  await worker.runOnce();
  expect(purges).toBe(1);
  expect(retries).toBe(1);
  expect(succeeded).toBe(false);
  failMark = false;
  await worker.runOnce();
  expect(purges).toBe(2);
  expect(marked).toBe(true);
  expect(succeeded).toBe(true);
});
