import { expect, test } from "bun:test";

import { ReleaseReconciler } from "./reconcile";

const ownerUserID = `usr_${"1".repeat(32)}`;
const appID = `app_${"2".repeat(32)}`;
const releaseID = `rel_${"3".repeat(32)}`;
const buildID = `sha256:${"4".repeat(64)}`;
const generation = `act_${"0".repeat(31)}1`;

function fixture(options = {}) {
  let queued = 0;
  const reconciler = new ReleaseReconciler({
    async activeReleaseCandidates() { return [{ ownerUserID, appID, releaseID }]; },
    async getForUser() { return { id: releaseID, appID, ...(options.preSDKOne ? {} : { definition: { app_name: "agent" } }), components: {
      api: { output: options.preSDKOne ? {} : { script_name: releaseID } },
      session_runtime: { output: { build_id: buildID } },
    } }; },
    async queueActiveReconciliation() { queued++; return true; },
  }, { async read() {
    if (options.unreadable) throw new Error("provider timeout");
    return { generation, closed: options.edgeClosed ?? false, script_name: releaseID };
  } }, { async releaseAdmission() { return { generation, closed: options.hostClosed ?? false }; },
    async activeBuild() { return options.wrongBuild ? `sha256:${"5".repeat(64)}` : buildID; } });
  return { reconciler, queued: () => queued };
}

test("healthy active release does not disturb admission on startup", async () => {
  const run = fixture();
  expect(await run.reconciler.runOnce()).toEqual({ checked: 1, queued: 0, unreadable: 0, unsupported: 0 });
  expect(run.queued()).toBe(0);
});

test("a live release that predates SDK 1.0 is reported, never queued", async () => {
  const run = fixture({ preSDKOne: true });
  expect(await run.reconciler.runOnce()).toEqual({ checked: 0, queued: 0, unreadable: 0, unsupported: 1 });
  expect(run.queued()).toBe(0);
});

for (const options of [{ edgeClosed: true }, { hostClosed: true }, { wrongBuild: true }]) {
  test(`queues only a mismatched App: ${Object.keys(options)[0]}`, async () => {
    const run = fixture(options);
    expect(await run.reconciler.runOnce()).toEqual({ checked: 1, queued: 1, unreadable: 0, unsupported: 0 });
    expect(run.queued()).toBe(1);
  });
}

test("an unreadable control endpoint is reported without a broad reactivation", async () => {
  const run = fixture({ unreadable: true });
  expect(await run.reconciler.runOnce()).toEqual({ checked: 0, queued: 0, unreadable: 1, unsupported: 0 });
  expect(run.queued()).toBe(0);
});
