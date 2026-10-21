import { expect, test } from "bun:test";

import { ComputeWorkspaceRegistrationWorker } from "./workspace-worker";

const operation = {
  id: "op_abcdef0123456789abcdef0123456789",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "register_workspace",
  status: "running",
  idempotencyKey: "register-workspace:wsp_abcdef0123456789abcdef0123456789",
  input: {
    workspace_id: "wsp_abcdef0123456789abcdef0123456789",
    workspace_slug: "staging",
  },
  attempts: 1,
};

test("registers a claimed Workspace", async () => {
  const calls = [];
  const worker = new ComputeWorkspaceRegistrationWorker(
    "control-worker-1",
    {
      async claim(id, kind) { calls.push(["claim", id, kind]); return operation; },
    },
    {
      async workspaceRegistrationContext(appID,workspaceID) { calls.push(["context",appID,workspaceID]); return {archived:false,computeTenantID:operation.ownerUserID }; },
      async succeedWorkspace(id, workerID, workspaceID) { calls.push(["succeed", id, workerID, workspaceID]); },
    },
    { async registerWorkspace(command) { calls.push(["register", command]); } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([
    ["claim", "control-worker-1", "register_workspace"],
    ["context", operation.appID,operation.input.workspace_id],
    ["register", {
      ownerUserID: operation.ownerUserID,
      appID: operation.appID,
      workspaceID: operation.input.workspace_id,
      workspaceSlug: "staging",
      registration:{computeTenantID:operation.ownerUserID },
    }],
    ["succeed", operation.id, "control-worker-1", operation.input.workspace_id],
  ]);
});

test("fails a registration for an archived Workspace without contacting the provider", async () => {
  const calls = [];
  const worker = new ComputeWorkspaceRegistrationWorker(
    "control-worker-1",
    {
      async claim() { return operation; },
      async fail(id, workerID, code) { calls.push(["fail", id, workerID, code]); },
    },
    {
      async workspaceRegistrationContext() { return {archived:true}; },
      async succeedWorkspace() { calls.push(["succeed"]); },
    },
    { async registerWorkspace() { calls.push(["register"]); } },
  );
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toEqual([["fail", operation.id, "control-worker-1", "workspace_archived"]]);
});
