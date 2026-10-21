import { expect, test } from "bun:test";

import { ComputeWorkspaceArchiveWorker } from "./workspace-archive-worker";

const operation = {
  id: "op_abcdef0123456789abcdef0123456789",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "archive_workspace",
  status: "running",
  idempotencyKey: "archive-workspace:wsp_abcdef0123456789abcdef0123456789",
  input: { workspace_id: "wsp_abcdef0123456789abcdef0123456789" },
  attempts: 1,
};

test("cancels a queued registration before archiving its Workspace", async () => {
  const calls = [];
  let running = true;
  const worker = new ComputeWorkspaceArchiveWorker(
    "control-worker-1",
    {
      async claim(workerID, kind) { calls.push(["claim", workerID, kind]); return operation; },
      async cancelQueuedWorkspaceRegistration(...args) { calls.push(["cancel", ...args]); },
      async hasRunningWorkspaceRegistration(...args) { calls.push(["running", ...args]); return running; },
      async retry(_id, _worker, at) { calls.push(["retry", at]); },
      async succeed(...args) { calls.push(["succeed", ...args]); },
    },
    { async archiveWorkspace(command) { calls.push(["archive", command]); } },
    () => new Date("2026-08-12T12:00:00Z"),
  );

  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["claim", "control-worker-1", "archive_workspace"],
    ["cancel", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["running", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["retry", new Date("2026-08-12T12:00:30Z")],
  ]);

  calls.length = 0;
  running = false;
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["claim", "control-worker-1", "archive_workspace"],
    ["cancel", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["running", operation.ownerUserID, operation.appID, operation.input.workspace_id],
    ["archive", {
      ownerUserID: operation.ownerUserID,
      appID: operation.appID,
      workspaceID: operation.input.workspace_id,
    }],
    ["succeed", operation.id, "control-worker-1", { provider_workspace_id: operation.input.workspace_id }],
  ]);
});
