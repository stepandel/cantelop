import { expect, test } from "bun:test";

import { ReleaseActivationWorker } from "./worker";

const ownerUserID = `usr_${"1".repeat(32)}`;
const appID = `app_${"2".repeat(32)}`;
const previousID = `rel_${"3".repeat(32)}`;
const targetID = `rel_${"4".repeat(32)}`;
const generation = `act_${"0".repeat(31)}1`;
const buildID = `sha256:${"a".repeat(64)}`;
const oldBuildID = `sha256:${"b".repeat(64)}`;
const operation = {
  id: `op_${"5".repeat(32)}`, ownerUserID, appID, kind: "activate_release",
  status: "running", lockedBy: "activation-worker", attempts: 1,
  input: { target_release_id: targetID, previous_release_id: previousID, generation },
};

function fixture(failAt, targetStatus = "provisioning", options = {}) {
  const calls = [];
  let failOnce = true;
  let dbActive = previousID;
  let edge = { app_id: appID, generation: null, closed: true, script_name: previousID };
  let host = { generation: "", closed: true };
  let activeBuild = oldBuildID;
  let pending = true;
  const maybeFail = (stage) => {
    calls.push(stage);
    if (failOnce && failAt === stage) { failOnce = false; throw new Error("injected provider failure"); }
  };
  const target = options.target ?? {
    id: targetID, appID, status: targetStatus, apiArtifactDigest: `sha256:${"c".repeat(64)}`,
    components: {
      api: { status: "succeeded", output: { script_name: targetID } },
      session_runtime: { status: "succeeded", output: { build_id: buildID } },
    },
  };
  const worker = new ReleaseActivationWorker("activation-worker",
    {
      async claim() { if (!pending) return undefined; return operation; },
      async getForUser() { return operation; },
      async retry() { calls.push("retry"); },
      async fail(_id, _worker, code) { calls.push(code); pending = false; },
      async succeed() { maybeFail("succeed"); pending = false; },
    },
    {
      async deploymentManifest() { return "manifest" in options ? options.manifest : { kind: "cantelop-protocol-edge", environment: {} }; },
      async withDeploymentLock(_app, action) { return action(); },
      async getForUser() { return target; },
      async commitActivation(_owner, _app, wanted, previous) {
        maybeFail("commit");
        expect(wanted).toBe(targetID);
        expect(previous).toBe(previousID);
        expect(dbActive === previousID || dbActive === targetID).toBe(true);
        dbActive = targetID;
      },
    },
    {
      async close(_app, gen) { maybeFail("edge_close"); edge = { ...edge, generation: gen, closed: true }; return edge; },
      async select(_app, gen, script) { maybeFail("edge_select"); expect(edge).toMatchObject({ generation: gen, closed: true }); edge = { ...edge, script_name: script }; return edge; },
      async open(_app, gen) { maybeFail("edge_open"); expect(edge.generation).toBe(gen); edge = { ...edge, closed: false }; return edge; },
      async read() { maybeFail("edge_read"); return edge; },
    },
    {
      async setReleaseAdmission(_owner, _app, gen, closed) {
        maybeFail(closed ? "host_close" : "host_open"); host = { generation: gen, closed };
      },
      async releaseAdmission() { maybeFail("host_read"); return host; },
      async selectBuild(_owner, _app, build) { maybeFail("build_select"); expect(host.closed).toBe(true); activeBuild = build; },
      async activeBuild() { maybeFail("build_read"); return activeBuild; },
      ...(options.retainsBuild ? { retainsBuild: options.retainsBuild } : {}),
    },
    { async projection() { return options.projection ?? { revision: 1, values: [{ name: "SECRET", kind: "secret", value: "current" }], systemBindings: [{ name: "CANTELOP_INTEGRATION_TOKEN", kind: "secret", value: "token" }] }; } },
    { async replaceConfiguration(projection) { maybeFail("config"); expect(projection.scriptName).toBe(targetID); } },
  );
  return { worker, calls, state: () => ({ dbActive, edge, host, activeBuild, pending }) };
}

test("activation changes both provider pointers behind closed gates, then commits and opens", async () => {
  const run = fixture();
  expect(await run.worker.runOnce()).toBe(true);
  expect(run.state()).toMatchObject({ dbActive: targetID, pending: false,
    edge: { closed: false, script_name: targetID }, host: { closed: false }, activeBuild: buildID });
  expect(run.calls.indexOf("edge_close")).toBeLessThan(run.calls.indexOf("build_select"));
  expect(run.calls.indexOf("host_close")).toBeLessThan(run.calls.indexOf("build_select"));
  expect(run.calls.indexOf("commit")).toBeLessThan(run.calls.indexOf("edge_open"));
});

for (const stage of ["edge_close", "host_close", "edge_read", "host_read", "config", "build_select",
  "edge_select", "build_read", "commit", "host_open", "edge_open", "succeed"]) {
  test(`interrupted activation at ${stage} converges on retry`, async () => {
    const run = fixture(stage);
    await run.worker.runOnce();
    expect(run.calls).toContain("retry");
    const interrupted = run.state();
    if (interrupted.dbActive === previousID &&
        (interrupted.edge.script_name === targetID || interrupted.activeBuild === buildID)) {
      expect(interrupted.edge.closed).toBe(true);
      expect(interrupted.host.closed).toBe(true);
    }
    await run.worker.runOnce();
    expect(run.state()).toMatchObject({ dbActive: targetID, pending: false,
      edge: { closed: false, script_name: targetID }, host: { closed: false }, activeBuild: buildID });
  });
}

test("a release that predates SDK 1.0 fails terminally without touching either gate", async () => {
  for (const options of [
    // A pre-1.0 release ran under the App's stable script, never its own.
    { target: { id: targetID, appID, status: "superseded", components: {
      api: { status: "succeeded", output: { provider_release_id: "old-tag" } },
      session_runtime: { status: "succeeded", output: { build_id: buildID } } } } },
    // Its customer-authored API manifest has no SDK 1.0 Edge counterpart.
    { manifest: null },
  ]) {
    const { worker, calls, state } = fixture(undefined, "superseded", options);
    await worker.runOnce();
    expect(calls).toContain("release_unsupported");
    expect(calls).not.toContain("edge_close");
    expect(calls).not.toContain("host_close");
    expect(state().dbActive).toBe(previousID);
  }
});

test("migration failure leaves the existing release admitted and does not select a new build", async () => {
  const run = fixture();
  run.worker.migrateDatabase = async () => { throw new Error("migration unavailable"); };
  await run.worker.runOnce();
  expect(run.calls).toContain("retry");
  expect(run.calls).not.toContain("edge_close");
  expect(run.calls).not.toContain("host_close");
  expect(run.calls).not.toContain("build_select");
});
test("migration completes before either admission path closes", async () => {
  const run = fixture();
  run.worker.migrateDatabase = async () => { run.calls.push("schema"); };
  await run.worker.runOnce();
  expect(run.calls.indexOf("schema")).toBeLessThan(run.calls.indexOf("edge_close"));
  expect(run.calls.indexOf("schema")).toBeLessThan(run.calls.indexOf("host_close"));
});


test("missing App configuration fails before closing release gates", async () => {
  for (const environment of [{ API_TOKEN: { required: true, secret: true } }, {}]) {
    const { worker, calls, state } = fixture(undefined, "provisioning", {
      manifest: { kind: "cantelop-protocol-edge", environment },
      projection: { revision: 0, values: [] },
    });
    await worker.runOnce();
    expect(calls).toEqual(["edge_read", "app_environment_missing_or_invalid"]);
    expect(state().dbActive).toBe(previousID);
  }
});

test("a target Build the host no longer retains fails before closing either gate", async () => {
  const { worker, calls, state } = fixture(undefined, "superseded", {
    async retainsBuild(_owner, _app, build) { expect(build).toBe(buildID); return false; },
  });
  await worker.runOnce();
  expect(calls).toContain("target_build_missing");
  expect(calls).not.toContain("edge_close");
  expect(calls).not.toContain("host_close");
  expect(state()).toMatchObject({ dbActive: previousID, activeBuild: oldBuildID });
});

test("an unknown Build retention answer falls through to Build selection", async () => {
  const { worker, calls, state } = fixture(undefined, "superseded", {
    async retainsBuild() { throw new Error("older ingress"); },
  });
  await worker.runOnce();
  expect(calls).not.toContain("target_build_missing");
  expect(state().dbActive).toBe(targetID);
});
