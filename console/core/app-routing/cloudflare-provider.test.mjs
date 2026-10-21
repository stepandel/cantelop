import { expect, test } from "bun:test";

import { AppRoutingProviderError } from "./provider";
import { CloudflareAppRoutingProvider } from "./cloudflare-provider";

const command = {
  appID: "app_0123456789abcdef0123456789abcdef",
  appSlug: "agent",
};

function provider(request) {
  return new CloudflareAppRoutingProvider({
    accountID: "a".repeat(32),
    dispatchNamespace: "customer-workers",
    routeNamespaceID: "b".repeat(32),
    apiToken: "token",
    apiBaseURL: "https://cloudflare.example.test/client/v4",
  }, request);
}

test("publishes the stable App ID under its public slug", async () => {
  let received;
  await provider(async (url, init) => {
    received = { url, init };
    return Response.json({ success: true });
  }).publish(command);

  expect(received.url).toEndWith(`/storage/kv/namespaces/${"b".repeat(32)}/values/agent`);
  expect(received.init.method).toBe("PUT");
  expect(received.init.body).toBe(command.appID);
  expect(received.init.headers.Authorization).toBe("Bearer token");
});

test("deletes only the archived App's Worker, leaving the reusable slug key untouched", async () => {
  const calls = [];
  await provider(async (url, init = {}) => {
    calls.push([url, init.method ?? "GET"]);
    return Response.json({ success: true });
  }).remove(command);

  expect(calls.map(([, method]) => method)).toEqual(["DELETE"]);
  expect(calls[0][0]).toEndWith(`/workers/dispatch/namespaces/customer-workers/scripts/${command.appID}`);
});

test("leaves the route intact when customer Worker deletion fails", async () => {
  const calls = [];
  const routing = provider(async (url, init = {}) => {
    calls.push([url, init.method ?? "GET"]);
    return Response.json({ success: false }, { status: 503 });
  });
  await expect(routing.remove(command)).rejects.toEqual(new AppRoutingProviderError(true));
  expect(calls.map(([, method]) => method)).toEqual(["DELETE"]);
  expect(calls[0][0]).toContain("/scripts/");
});

test("old cleanup cannot delete a new App's route during slug reuse", async () => {
  let routeOwner = command.appID;
  const routing = provider(async (url, init = {}) => {
    if (init.method === "DELETE" && url.includes("/scripts/")) {
      routeOwner = "app_abcdef0123456789abcdef0123456789";
      return Response.json({ success: true });
    }
    if (url.includes("/values/")) throw new Error("old cleanup touched the reused slug");
    throw new Error("unexpected Cloudflare call");
  });
  await routing.remove(command);
  expect(routeOwner).toBe("app_abcdef0123456789abcdef0123456789");
});

test("treats an already absent route and Worker as archived", async () => {
  let calls = 0;
  await provider(async () => {
    calls += 1;
    return new Response(null, { status: 404 });
  }).remove(command);
  expect(calls).toBe(1);
});
