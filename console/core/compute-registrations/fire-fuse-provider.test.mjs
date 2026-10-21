import { expect, test } from "bun:test";

import { FireFuseComputeRegistrationProvider } from "./fire-fuse-provider";

const token = "a".repeat(64);
const command = {
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  appSlug: "agent",
  internalPort: 3000,
};

test("registers exact control-plane resource identities with Fire Fuse", async () => {
  let captured;
  const provider = new FireFuseComputeRegistrationProvider({ ingressOrigin: "https://compute.example.test", token }, async (url, init) => {
    captured = { url, init, body: JSON.parse(init.body) };
    return Response.json({ app_id: command.appID, status: "registered" }, { status: 201 });
  });
  await provider.registerApp(command);
  expect(captured.url).toBe(`https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}`);
  expect(captured.init.method).toBe("PUT");
  expect(captured.init.headers.authorization).toBe(`Bearer ${token}`);
  expect(captured.body).toEqual({
    slug: "agent",
    session_runtime: {
      runtime: { internal_port: 3000, profile: "default" },
    },
  });
});

test("registers, archives, and purges Workspaces and archives Apps through desired-state routes", async () => {
  const calls = [];
  const provider = new FireFuseComputeRegistrationProvider({ ingressOrigin: "https://compute.example.test", token }, async (url, init) => {
    calls.push({ url, init, body: init.body ? JSON.parse(init.body) : undefined });
    if (init.method === "DELETE" && url.endsWith("/purge")) {
      return Response.json({ workspace_id: "wsp_abcdef0123456789abcdef0123456789", status: "purged" });
    }
    if (init.method === "DELETE" && url.includes("/workspaces/")) {
      return Response.json({ workspace_id: "wsp_abcdef0123456789abcdef0123456789", status: "archived" });
    }
    if (init.method === "DELETE") return Response.json({ app_id: command.appID, status: "archived" });
    return Response.json({ workspace_id: "wsp_abcdef0123456789abcdef0123456789", status: "registered" }, { status: 201 });
  });
  await provider.registerWorkspace({
    ownerUserID: command.ownerUserID,
    appID: command.appID,
    workspaceID: "wsp_abcdef0123456789abcdef0123456789",
    workspaceSlug: "staging",
  });
  await provider.archiveWorkspace({
    ownerUserID: command.ownerUserID,
    appID: command.appID,
    workspaceID: "wsp_abcdef0123456789abcdef0123456789",
  });
  await provider.purgeWorkspace({
    ownerUserID: command.ownerUserID,
    appID: command.appID,
    workspaceID: "wsp_abcdef0123456789abcdef0123456789",
  });
  await provider.archiveApp({ ownerUserID: command.ownerUserID, appID: command.appID });
  expect(calls[0]).toMatchObject({
    url: `https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}/workspaces/wsp_abcdef0123456789abcdef0123456789`,
    body: { slug: "staging" },
  });
  expect(calls[1]).toMatchObject({
    url: `https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}/workspaces/wsp_abcdef0123456789abcdef0123456789`,
    init: { method: "DELETE" },
  });
  expect(calls[2]).toMatchObject({
    url: `https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}/workspaces/wsp_abcdef0123456789abcdef0123456789/purge`,
    init: { method: "DELETE" },
  });
  expect(calls[3]).toMatchObject({
    url: `https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}`,
    init: { method: "DELETE" },
  });
});

test("replaces the revisioned Fire Fuse runtime configuration cache", async () => {
  let captured;
  const provider = new FireFuseComputeRegistrationProvider({ ingressOrigin: "https://compute.example.test", token }, async (url, init) => {
    captured = { url, body: JSON.parse(init.body) };
    return Response.json({ app_id: command.appID, revision: 4, status: "applied" });
  });
  await provider.replaceConfiguration({
    ownerUserID: command.ownerUserID,
    appID: command.appID,
    revision: 4,
    values: [{ name: "TOKEN", kind: "secret", value: "secret" }],
  });
  expect(captured.url).toBe(`https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}/configuration`);
  expect(captured.body).toEqual({ revision: 4, values: [{ name: "TOKEN", kind: "secret", value: "secret" }] });
});


test("worker registration uses its canonical tenant snapshot without another SQL lookup",async () => {
  let url;
  const workspaceID="wsp_abcdef0123456789abcdef0123456789";
  const tenant="usr_1123456789abcdef0123456789abcdef";
  const provider=new FireFuseComputeRegistrationProvider({ingressOrigin:"https://compute.example.test",token},async (input)=>{
    url=input;return Response.json({workspace_id:workspaceID,status:"registered"});
  },async ()=>{throw new Error("redundant tenant lookup");});
  await provider.registerWorkspace({ownerUserID:command.ownerUserID,appID:command.appID,workspaceID,workspaceSlug:"staging",registration:{computeTenantID:tenant,databaseRequired:false}});
  expect(url).toContain(`/users/${tenant}/`);
});
