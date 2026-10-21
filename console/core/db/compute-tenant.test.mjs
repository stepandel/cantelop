import { expect, test } from "bun:test";
import { computeTenantResolver } from "./compute-tenant";
import { FireFuseComputeRegistrationProvider } from "../compute-registrations/fire-fuse-provider";
import { FireFuseSessionRuntimeProvider } from "../session-runtime-materializations/fire-fuse-provider";
const ownerID = `usr_${"1".repeat(32)}`;
const tenantID = `usr_${"2".repeat(32)}`;
const appID = `app_${"3".repeat(32)}`;
const workspaceID = `wsp_${"4".repeat(32)}`;
const commandID = `op_${"5".repeat(32)}`;
const config = { ingressOrigin: "http://127.0.0.1:8084", token: "a".repeat(64) };

test("provider addressing resolves persisted tenant keys, independent of product user IDs", async () => {
  const resolve = computeTenantResolver({ async query(sql, values) {
    expect(sql).toContain("coalesce(compute_tenant_id, owner_user_id)");
    expect(values).toEqual([appID]);
    return { rows: [{ tenant_id: tenantID }], rowCount: 1 };
  } });
  expect(await resolve(appID, ownerID)).toBe(tenantID);
  await expect(computeTenantResolver({ query: async () => ({ rows: [], rowCount: 0 }) })(appID, ownerID)).rejects.toMatchObject({ code: "not_found" });
});

test("registration, configuration, workspace, archive, build and cancellation use the preserved tenant", async () => {
  const paths = [];
  const resolve = async (app, owner) => { expect(app).toBe(appID); expect(owner).toBe(ownerID); return tenantID; };
  let receipt;
  const request = async (url) => { paths.push(new URL(url).pathname); return Response.json(receipt); };
  const registrations = new FireFuseComputeRegistrationProvider(config, request, resolve);
  receipt = { app_id: appID, status: "registered" };
  await registrations.registerApp({ ownerUserID: ownerID, appID, appSlug: "example", internalPort: 3000 });
  receipt = { workspace_id: workspaceID, status: "registered" };
  await registrations.registerWorkspace({ ownerUserID: ownerID, appID, workspaceID, workspaceSlug: "production" });
  receipt = { app_id: appID, revision: 1, status: "applied" };
  await registrations.replaceConfiguration({ ownerUserID: ownerID, appID, revision: 1, values: [] });
  receipt = { app_id: appID, status: "archived" };
  await registrations.archiveApp({ ownerUserID: ownerID, appID });
  const runtime = new FireFuseSessionRuntimeProvider(config, request, resolve);
  receipt = { id: `bop_${"5".repeat(32)}`, app_id: appID, status: "receiving" };
  await runtime.materialize({ ownerUserID: ownerID, appID, commandID,
    imageURL: `https://store.example.com/prepared-images/sha256/${"a".repeat(64)}.ext4?token=test`,
    manifest: { schema_version: 1, build_id: `sha256:${"b".repeat(64)}`,
      image_key: `prepared-images/sha256/${"a".repeat(64)}.ext4`, rootfs_sha256: "a".repeat(64),
      rootfs_size_bytes: 100, process: { args: ["/app"], env: [], working_directory: "/", uid: 10001, gid: 10001 } } });
  receipt = { ...receipt, status: "failed" };
  await runtime.cancel(ownerID, appID, commandID);
  expect(paths).toHaveLength(6);
  for (const path of paths) expect(path).toStartWith(`/v1/internal/users/${tenantID}/apps/${appID}`);
});
