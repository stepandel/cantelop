import { expect, test } from "bun:test";

import { FireFuseSessionRuntimeProvider } from "./fire-fuse-provider";
import { SessionRuntimeProviderError } from "./provider";

const token = "a".repeat(64);
const command = {
  commandID: "op_0123456789abcdef0123456789abcdef",
  ownerUserID: "usr_0123456789abcdef0123456789abcdef",
  appID: "app_0123456789abcdef0123456789abcdef",
  imageURL: "https://artifacts.example.test/prepared-images/sha256/" + "b".repeat(64) + ".ext4?X-Amz-Signature=signed",
  manifest: { schema_version: 1, build_id: `sha256:${"c".repeat(64)}`,
    image_key: `prepared-images/sha256/${"b".repeat(64)}.ext4`, rootfs_sha256: "b".repeat(64),
    rootfs_size_bytes: 4096, process: { args: ["/app"], env: [], working_directory: "/", uid: 10001, gid: 10001 } },
};

test("submits an authenticated idempotent materialization command", async () => {
  let observed;
  const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async (url, init) => {
    observed = { url, init, body: JSON.parse(init.body) };
    return Response.json({
      id: "bop_0123456789abcdef0123456789abcdef",
      app_id: command.appID,
      status: "succeeded",
      build_id: `sha256:${"c".repeat(64)}`,
      build_version: 3,
    });
  });
  expect(await provider.materialize(command)).toEqual({
    operationID: "bop_0123456789abcdef0123456789abcdef",
    status: "succeeded",
    buildID: `sha256:${"c".repeat(64)}`,
    buildVersion: 3,
  });
  expect(observed.url).toBe(`https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}/prepared-builds`);
  expect(observed.init.headers.authorization).toBe(`Bearer ${token}`);
  expect(observed.body).toEqual({
    command_id: command.commandID,
    image_url: command.imageURL,
    manifest: command.manifest,
  });
});

test("retries not found while independent App registration converges", async () => {
  const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async () =>
    Response.json({ error: "private detail" }, { status: 404 }));
  await expect(provider.materialize(command)).rejects.toMatchObject({ retryable: true });
});

test("fails a retention quota without treating it as rate limiting", async () => {
  const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async () =>
    Response.json({ error: { code: "build_retention_quota", message: "quota exceeded" } }, { status: 429 }));
  await expect(provider.materialize(command)).rejects.toEqual(new SessionRuntimeProviderError(false, "build_retention_quota"));
});

test("fails host disk capacity without retrying", async () => {
  const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async () =>
    Response.json({ error: { code: "host_disk_capacity", message: "host image storage is full" } }, { status: 507 }));
  await expect(provider.materialize(command)).rejects.toEqual(new SessionRuntimeProviderError(false, "host_disk_capacity", "provider_http_507"));
});

test("ordinary and malformed rate limits still retry", async () => {
  for (const body of [JSON.stringify({ error: { code: "rate_limited" } }), "upstream throttled"]) {
    const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async () =>
      new Response(body, { status: 429 }));
    await expect(provider.materialize(command)).rejects.toMatchObject({ retryable: true });
  }
});

test("provider cancellation requires a terminal receipt for the exact command", async () => {
 let url;
 let document={id:"bop_0123456789abcdef0123456789abcdef",app_id:command.appID,status:"failed",error_code:"deployment_cancelled"};
 const provider=new FireFuseSessionRuntimeProvider({ingressOrigin:"https://compute.example.test",token},async (u, init)=>{
  url=u;expect(init.method).toBe("DELETE");return Response.json(document);
 });
 await provider.cancel(command.ownerUserID,command.appID,command.commandID);
 expect(url.endsWith(`/prepared-builds/${command.commandID}`)).toBe(true);
 document={...document,status:"processing"};
 await expect(provider.cancel(command.ownerUserID,command.appID,command.commandID)).rejects.toThrow();
 document={...document,status:"failed",app_id:"app_"+"f".repeat(32)};
 await expect(provider.cancel(command.ownerUserID,command.appID,command.commandID)).rejects.toThrow();
});

test("selects an owned retained Build and checks the provider receipt", async () => {
  const calls = [];
  const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async (url, init) => {
    calls.push({ url, init });
    return Response.json({ app_id: command.appID, build_id: JSON.parse(init.body).build_id });
  });
  await provider.selectBuild(command.ownerUserID, command.appID, command.manifest.build_id);
  expect(calls[0].url).toBe(`https://compute.example.test/v1/internal/users/${command.ownerUserID}/apps/${command.appID}/active-build`);
  expect(calls[0].init.method).toBe("PUT");
  expect(JSON.parse(calls[0].init.body)).toEqual({ build_id: command.manifest.build_id });
  await provider.selectBuild(command.ownerUserID, command.appID, null);
  expect(JSON.parse(calls[1].init.body)).toEqual({ build_id: null });
  await expect(provider.selectBuild(command.ownerUserID, command.appID, "sha256:invalid")).rejects.toThrow(TypeError);
  expect(calls).toHaveLength(2);
  const badReceipt = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async () =>
    Response.json({ app_id: command.appID, build_id: `sha256:${"f".repeat(64)}` }));
  await expect(badReceipt.selectBuild(command.ownerUserID, command.appID, command.manifest.build_id)).rejects.toMatchObject({ retryable: true });
});

test("reads and closes the owner-scoped host gate before Build selection", async () => {
  const generation = `act_${"0".repeat(31)}1`;
  const calls = [];
  const provider = new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async (url, init) => {
    calls.push({ url, init });
    if (url.endsWith("/active-build")) return Response.json({ app_id: command.appID, build_id: command.manifest.build_id });
    return Response.json({ generation, closed: init.method === "PUT" ? JSON.parse(init.body).closed : true });
  });
  await provider.setReleaseAdmission(command.ownerUserID, command.appID, generation, true);
  expect(await provider.releaseAdmission(command.ownerUserID, command.appID)).toEqual({ generation, closed: true });
  expect(await provider.activeBuild(command.ownerUserID, command.appID)).toBe(command.manifest.build_id);
  expect(calls[0].url).toEndWith(`/users/${command.ownerUserID}/apps/${command.appID}/release-admission`);
  expect(calls[0].init.headers.authorization).toBe(`Bearer ${token}`);
  expect(JSON.parse(calls[0].init.body)).toEqual({ generation, closed: true });
});

test("reports whether the host still retains a Build, and only trusts explicit answers", async () => {
  const owner = `usr_${"1".repeat(32)}`;
  const app = `app_${"2".repeat(32)}`;
  const build = `sha256:${"a".repeat(64)}`;
  const provider = (respond) => new FireFuseSessionRuntimeProvider({ ingressOrigin: "https://compute.example.test", token }, async (url) => {
    expect(String(url)).toBe(`https://compute.example.test/v1/internal/users/${owner}/apps/${app}/builds/${build}`);
    return respond();
  });
  expect(await provider(() => Response.json({ app_id: app, build_id: build, version: 3 })).retainsBuild(owner, app, build)).toBe(true);
  expect(await provider(() => Response.json({ error: { code: "build_not_found" } }, { status: 404 })).retainsBuild(owner, app, build)).toBe(false);
  await expect(provider(() => new Response("404 page not found", { status: 404 })).retainsBuild(owner, app, build)).rejects.toMatchObject({ retryable: true });
});
