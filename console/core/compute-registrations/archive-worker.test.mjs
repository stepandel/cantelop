import { expect, test } from "bun:test";

import { ComputeAppArchiveWorker } from "./archive-worker";
import { SessionRuntimeProviderError } from "../session-runtime-materializations/provider";

const operation = {
  id: "op_abcdef0123456789abcdef0123456789",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  kind: "archive_app",
  status: "running",
  idempotencyKey: "archive-app:app_0123456789abcdef0123456789abcdef",
  input: { app_slug: "agent" },
  attempts: 1,
};
const generation = "act_00000000000000000000000000000001";

function harness(unfinished = new Set()) {
  const calls = [];
  const state = { archived: false, failRemoval: false, failHostArchive: false, hostMissing: false };
  const operations = {
    async claim() { return operation; },
    async hasUnfinished(_userID, _appID, kind) { calls.push(`check:${kind}`); return unfinished.has(kind); },
    async retry() { calls.push("retry"); },
    async fail() { calls.push("fail"); },
    async succeed() { calls.push("succeed"); },
  };
  const products = {
    async nextArchiveGeneration() { calls.push("generation"); return generation; },
    async completeArchive() { calls.push("archive-db"); state.archived = true; },
    async releaseScriptIDs() { calls.push("list-scripts"); return ["rel_0123456789abcdef0123456789abcdef"]; },
  };
  const edge = {
    async close() { calls.push("edge-close"); return { generation, closed: true }; },
    async read() { calls.push("edge-read"); return { generation, closed: true }; },
  };
  const runtime = {
    async setReleaseAdmission() {
      calls.push("host-close");
      if (state.hostMissing) throw new SessionRuntimeProviderError(true, undefined, "provider_http_404");
    },
    async releaseAdmission() { calls.push("host-read"); return { generation, closed: true }; },
  };
  const routing = { async remove() { calls.push("remove-stable"); } };
  const api = { async removeRelease() { calls.push("remove-release"); if (state.failRemoval) throw new Error("provider unavailable"); } };
  const provider = { async archiveApp() { calls.push("archive-host"); if (state.failHostArchive) throw new Error("compute unavailable"); } };
  const releases = { async withDeploymentLock(_appID, action) { calls.push("lock"); return action(); } };
  const worker = new ComputeAppArchiveWorker("control-worker-1", operations, provider, routing,
    products, releases, edge, runtime, api, () => new Date("2026-08-12T12:00:00Z"));
  return { worker, calls, state };
}

test("waits for provider work, then closes both gates before marking archived or deleting scripts", async () => {
  const unfinished = new Set(["register_app"]);
  const { worker, calls, state } = harness(unfinished);
  expect(await worker.runOnce()).toBe(true);
  expect(state.archived).toBe(false);
  expect(calls).toEqual(["lock", "check:register_app", "retry"]);

  unfinished.clear();
  calls.length = 0;
  expect(await worker.runOnce()).toBe(true);
  expect(state.archived).toBe(true);
  expect(calls).toEqual([
    "lock", "check:register_app", "check:register_workspace", "check:archive_workspace",
    "check:purge_workspace", "check:deploy_api", "check:materialize_build", "check:attach_package", "check:sync_config",
    "generation", "edge-close", "edge-read", "host-close", "host-read", "archive-host",
    "archive-db", "remove-stable", "list-scripts", "remove-release", "succeed",
  ]);
});

test("keeps retrying cleanup behind closed gates after an uncertain release-script removal", async () => {
  const { worker, calls, state } = harness();
  state.failRemoval = true;
  expect(await worker.runOnce()).toBe(true);
  expect(state.archived).toBe(true);
  expect(calls.at(-1)).toBe("retry");
  expect(calls).toContain("archive-host");
  state.failRemoval = false;
  calls.length = 0;
  expect(await worker.runOnce()).toBe(true);
  expect(calls.at(-1)).toBe("succeed");
});

test("does not mark an App archived until compute termination succeeds", async () => {
  const { worker, calls, state } = harness();
  state.failHostArchive = true;
  expect(await worker.runOnce()).toBe(true);
  expect(state.archived).toBe(false);
  expect(calls.at(-1)).toBe("retry");
  expect(calls).not.toContain("archive-db");
});

test("a missing compute registration is already closed and cannot block archival", async () => {
  const { worker, calls, state } = harness();
  state.hostMissing = true;
  expect(await worker.runOnce()).toBe(true);
  expect(calls).toContain("archive-db");
  expect(calls).not.toContain("host-read");
});
