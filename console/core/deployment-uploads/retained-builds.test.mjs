import { expect, test } from "bun:test";

import { observedRetainedBuilds } from "./retained-builds";

const appID = `app_${"1".repeat(32)}`;
const build = `sha256:${"a".repeat(64)}`;
const environment = { FIRE_FUSE_OBSERVER_URL: "https://observer.example.test", FIRE_FUSE_STATUS_TOKEN: "t".repeat(32) };

test("reads retained Build IDs through the authenticated observer", async () => {
  let seen;
  const builds = await observedRetainedBuilds(appID, environment, async (url, init) => {
    seen = { url: String(url), authorization: init.headers.Authorization };
    return Response.json({ app_id: appID, builds: [{ version: 2, id: build }] });
  });
  expect(builds).toEqual(new Set([build]));
  expect(seen).toEqual({ url: `https://observer.example.test/apps/${appID}/builds`, authorization: `Bearer ${"t".repeat(32)}` });
});

test("an App the host does not know retains no Builds", async () => {
  expect(await observedRetainedBuilds(appID, environment, async () =>
    Response.json({ app_id: appID, error: { code: "app_not_found" } }, { status: 404 }))).toEqual(new Set());
});

test("proxy and unsupported-route 404s leave retention unknown", async () => {
  for (const respond of [
    async () => new Response("404 page not found", { status: 404 }),
    async () => new Response("<html><title>404 Not Found</title></html>", { status: 404 }),
    async () => Response.json({ error: { code: "not_found" } }, { status: 404 }),
    async () => Response.json({ app_id: appID, error: { code: "not_found" } }, { status: 404 }),
    async () => Response.json({ app_id: `app_${"2".repeat(32)}`, error: { code: "app_not_found" } }, { status: 404 }),
  ]) {
    expect(await observedRetainedBuilds(appID, environment, respond)).toBeNull();
  }
});

test("an unconfigured, failing, or malformed observer leaves retention unknown", async () => {
  expect(await observedRetainedBuilds(appID, {}, async () => { throw new Error("unreachable"); })).toBeNull();
  expect(await observedRetainedBuilds(appID, { ...environment, FIRE_FUSE_OBSERVER_URL: "http://observer.example.test" }, async () => Response.json({}))).toBeNull();
  for (const respond of [
    async () => { throw new Error("timeout"); },
    async () => new Response("bad gateway", { status: 502 }),
    async () => Response.json({ app_id: `app_${"2".repeat(32)}`, builds: [] }),
    async () => Response.json({ app_id: appID, builds: [{ id: "latest" }] }),
  ]) {
    expect(await observedRetainedBuilds(appID, environment, respond)).toBeNull();
  }
});
