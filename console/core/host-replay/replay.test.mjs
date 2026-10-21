import { expect, test } from "bun:test";
import { applyFreshHostReplay, planFreshHostReplay } from "./replay";

const appID = `app_${"a".repeat(32)}`;
const userID = `usr_${"b".repeat(32)}`;
const workspaceID = `wsp_${"c".repeat(32)}`;
const releaseID = `rel_${"d".repeat(32)}`;
const operationID = `op_${"e".repeat(32)}`;
const digest = "f".repeat(64);
const buildID = `sha256:${"1".repeat(64)}`;
const manifestKey = `prepared-images/manifests/${digest}.json`;

function database(buildVersion = 1, activeReleaseID = releaseID, internalPort = 3000, extraBuilds = []) {
  const queries = [];
  const client = {
    async query(sql) {
      queries.push(sql);
      if (sql.startsWith("SELECT count(*)") && sql.includes("operations")) return { rows: [{ count: "0" }] };
      if (sql.startsWith("SELECT count(*)") && sql.includes("releases")) return { rows: [{ count: "0" }] };
      if (sql.includes("FROM cantelop_control.apps a")) return { rows: [{
        id: appID, owner_user_id: userID, slug: "demo", status: "active",
        active_release_id: activeReleaseID, config_revision: "1", internal_port: internalPort,
      }] };
      if (sql.includes("FROM cantelop_control.workspaces")) return { rows: [{ id: workspaceID, app_id: appID, slug: "home" }] };
      if (sql.includes("FROM cantelop_control.release_components")) return { rows: [{
        app_id: appID, release_id: releaseID, operation_id: operationID,
        output: { build_id: buildID, build_version: buildVersion,
          prepared_manifest_key: manifestKey, prepared_manifest_digest: `sha256:${digest}` },
      }, ...extraBuilds] };
      return { rows: [] };
    },
    release() {},
  };
  return { connect: async () => client, queries };
}

test("plans active desired state from one consistent database snapshot", async () => {
  const db = database();
  const plan = await planFreshHostReplay(db);
  expect(plan).toHaveLength(1);
  expect(plan[0].builds[0].manifestKey).toBe(manifestKey);
  expect(plan[0].workspaces).toEqual([{ id: workspaceID, slug: "home" }]);
  expect(db.queries[0]).toContain("REPEATABLE READ READ ONLY");
  expect(db.queries.at(-1)).toBe("COMMIT");
});

test("rejects a Build history gap before changing the host", async () => {
  await expect(planFreshHostReplay(database(2))).rejects.toThrow("Build history gap");
});

test("rejects a release whose Build is not current", async () => {
  await expect(planFreshHostReplay(database(1, `rel_${"2".repeat(32)}`))).rejects.toThrow("no replayable active release");
});

test("refuses to omit an App missing its Session runtime", async () => {
  await expect(planFreshHostReplay(database(1, releaseID, null))).rejects.toThrow("no valid Session runtime");
});

test("replays Build, configuration, and Workspace only after App registration", async () => {
  const plan = await planFreshHostReplay(database());
  const calls = [];
  const admissions = [];
  const manifest = { schema_version: 1, build_id: buildID,
    image_key: `prepared-images/sha256/${digest}.ext4`, rootfs_sha256: digest,
    rootfs_size_bytes: 1024, process: { args: ["/app"], env: [], working_directory: "/", uid: 10001, gid: 10001 } };
  await applyFreshHostReplay(plan,
    {
      registerApp: async () => calls.push("app"),
      replaceConfiguration: async () => calls.push("config"),
      registerWorkspace: async () => calls.push("workspace"),
    },
    { materialize: async (command) => {
      expect(command.manifest).toEqual(manifest);
      calls.push("build");
      return { status: "succeeded", buildID, buildVersion: 1 };
    }, selectBuild: async (_owner, _app, selected) => {
      expect(selected).toBe(buildID);
      calls.push("select");
    }, releaseAdmission: async () => ({ generation: "", closed: false }),
    setReleaseAdmission: async (_owner, _app, _generation, closed) => { admissions.push(closed); },
    activeBuild: async () => buildID },
    { projection: async () => ({ revision: 1, values: [] }) },
    { readManifest: async () => manifest, signImageDownload: async () => "https://artifact.example/image?signature=ok" });
  expect(calls).toEqual(["app", "build", "select", "config", "workspace"]);
  expect(admissions).toEqual([true]);
});

test("replay selects the active release after restoring a newer Build", async () => {
  const newerReleaseID = `rel_${"2".repeat(32)}`;
  const newerOperationID = `op_${"3".repeat(32)}`;
  const newerBuildID = `sha256:${"4".repeat(64)}`;
  const newerDigest = "5".repeat(64);
  const newerKey = `prepared-images/manifests/${newerDigest}.json`;
  const extraBuild = { app_id: appID, release_id: newerReleaseID, operation_id: newerOperationID,
    output: { build_id: newerBuildID, build_version: 2,
      prepared_manifest_key: newerKey, prepared_manifest_digest: `sha256:${newerDigest}` } };
  const plan = await planFreshHostReplay(database(1, releaseID, 3000, [extraBuild]));
  const calls = [];
  let materialized = 0;
  await applyFreshHostReplay(plan,
    { registerApp: async () => {}, replaceConfiguration: async () => {}, registerWorkspace: async () => {} },
    { materialize: async (command) => {
      calls.push(command.manifest.build_id);
      materialized++;
      return { status: "succeeded", buildID: command.manifest.build_id, buildVersion: materialized };
    }, selectBuild: async (_owner, _app, selected) => calls.push(`select:${selected}`),
    releaseAdmission: async () => ({ generation: "", closed: false }),
    setReleaseAdmission: async () => {}, activeBuild: async () => buildID },
    { projection: async () => ({ revision: 1, values: [] }) },
    { readManifest: async (key) => ({ build_id: key === manifestKey ? buildID : newerBuildID,
      image_key: `prepared-images/sha256/${digest}.ext4` }),
    signImageDownload: async () => "https://artifact.example/image?signature=ok" });
  expect(calls).toEqual([buildID, `select:${buildID}`, newerBuildID, `select:${buildID}`]);
});

test("replay clears the Build when no release is active", async () => {
  const plan = await planFreshHostReplay(database(1, null));
  const selections = [];
  await applyFreshHostReplay(plan,
    { registerApp: async () => {}, replaceConfiguration: async () => {}, registerWorkspace: async () => {} },
    { materialize: async () => ({ status: "succeeded", buildID, buildVersion: 1 }),
      selectBuild: async (_owner, _app, selected) => selections.push(selected),
      releaseAdmission: async () => ({ generation: "", closed: false }),
      setReleaseAdmission: async () => {}, activeBuild: async () => null },
    { projection: async () => ({ revision: 1, values: [] }) },
    { readManifest: async () => ({ build_id: buildID, image_key: `prepared-images/sha256/${digest}.ext4` }),
      signImageDownload: async () => "https://artifact.example/image?signature=ok" });
  expect(selections).toEqual([null]);
});
