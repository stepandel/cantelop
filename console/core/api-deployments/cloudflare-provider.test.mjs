import { expect, test } from "bun:test";

import { CloudflareAPIProvider } from "./cloudflare-provider";

const appID = "app_0123456789abcdef0123456789abcdef";
const releaseID = "rel_0123456789abcdef0123456789abcdef";

test("stages the API module under its release ID without activating it", async () => {
  const requests = [];
  const provider = new CloudflareAPIProvider({
    accountID: "0123456789abcdef0123456789abcdef",
    dispatchNamespace: "cantelop-user-workers-staging",
    apiToken: "test-token",
    compatibilityDate: "2026-08-12",
    apiBaseURL: "https://api.example.test/client/v4",
    async fetch(url, init = {}) {
      requests.push({ url, init });
      return Response.json({ success: true, result: { id: appID, tag: "release-1", etag: "etag-1" } });
    },
  });
  expect(await provider.deploy(appID, releaseID, Buffer.from("export default { fetch() {} }"))).toEqual({ id: "release-1", etag: "etag-1", scriptName: releaseID });
  expect(requests).toHaveLength(1);
  expect(requests[0].url).toEndWith(`/scripts/${releaseID}`);
  expect(requests[0].init.headers.Authorization).toBe("Bearer test-token");
  const metadata = JSON.parse(await requests[0].init.body.get("metadata").text());
  expect(metadata.bindings).toEqual([]);
  expect(await requests[0].init.body.get("worker.mjs").text()).toBe("export default { fetch() {} }");
});

test("does not upload an invalid release identity", async () => {
  let calls = 0;
  const provider = new CloudflareAPIProvider({
    accountID: "0123456789abcdef0123456789abcdef",
    dispatchNamespace: "cantelop-user-workers-staging",
    apiToken: "test-token",
    compatibilityDate: "2026-08-12",
    async fetch() { calls++; return Response.json({ success: false }, { status: 503 }); },
  });
  await expect(provider.deploy(appID, appID, Buffer.from("export default {}"))).rejects.toThrow("invalid release ID");
  expect(calls).toBe(0);
});

test("does not expose provider response details on deployment failure", async () => {
  const provider = new CloudflareAPIProvider({
    accountID: "0123456789abcdef0123456789abcdef",
    dispatchNamespace: "cantelop-user-workers-staging",
    apiToken: "test-token",
    compatibilityDate: "2026-08-12",
    async fetch(_url, init = {}) {
      return Response.json({ success: false, errors: [{ message: "private provider detail" }] }, { status: 401 });
    },
  });
  await expect(provider.deploy(appID, releaseID, Buffer.from("export default {}"))).rejects.toThrow("deployment failed with HTTP 401");
});

test("removes only the selected release script and accepts an already missing script", async () => {
  const requests = [];
  const provider = new CloudflareAPIProvider({
    accountID: "0123456789abcdef0123456789abcdef",
    dispatchNamespace: "cantelop-user-workers-staging",
    apiToken: "test-token",
    compatibilityDate: "2026-08-12",
    async fetch(url, init) { requests.push({ url, init }); return new Response(null, { status: 404 }); },
  });
  await provider.removeRelease(appID, releaseID);
  expect(requests).toHaveLength(1);
  expect(requests[0].url).toEndWith(`/scripts/${releaseID}`);
  expect(requests[0].init.method).toBe("DELETE");
});
