import { afterEach, expect, mock, test } from "bun:test";
import { NextRequest } from "next/server";

let customerSession;
let createAppCalls = 0;
let createWorkspaceCalls = 0;
let configWrites = [];
mock.module("server-only", () => ({}));
mock.module("@/lib/user-session", () => ({ getUserSession: async () => customerSession }));
mock.module("@core/product-control", () => ({
  productControl: () => ({
    createApp: async () => { createAppCalls++; return { id: "app" }; },
    createWorkspace: async () => { createWorkspaceCalls++; return { id: "workspace" }; },
  }),
}));
mock.module("@core/configuration/control", () => ({
  configurationControl: () => ({
    setForUser: async (...args) => { configWrites.push(args); return { updated: true }; },
  }),
}));

const { POST: createApp } = await import("../app/api/customer/apps/route.ts");
const { POST: createWorkspace } = await import("../app/api/customer/workspaces/route.ts");
const { PUT: setConfig } = await import("../app/api/customer/config/[scope]/[id]/[kind]/[name]/route.ts");
const appID = `app_${"a".repeat(32)}`;
const configContext = { params: Promise.resolve({ scope: "apps", id: appID, kind: "variables", name: "TEST" }) };

afterEach(() => {
  customerSession = undefined;
  createAppCalls = 0;
  createWorkspaceCalls = 0;
  configWrites = [];
});

function request(path, body, method = "POST") {
  return new NextRequest(`https://console.cantelop.dev${path}`, {
    method,
    headers: { host: "console.cantelop.dev", origin: "https://console.cantelop.dev", "content-type": "application/json" },
    body,
    duplex: "half",
  });
}

function oversizedStream(limit) {
  let canceled = false;
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(limit));
      controller.enqueue(new Uint8Array([1]));
    },
    cancel() { canceled = true; },
  });
  return { body, wasCanceled: () => canceled };
}

test("App and Workspace creation stop oversized streams before control mutation", async () => {
  customerSession = { user: { id: `usr_${"b".repeat(32)}` } };
  for (const [path, limit, handler] of [
    ["/api/customer/apps", 4 * 16 * 1024, createApp],
    ["/api/customer/workspaces", 4 * 4 * 1024, createWorkspace],
  ]) {
    const stream = oversizedStream(limit);
    expect((await handler(request(path, stream.body))).status).toBe(400);
    expect(stream.wasCanceled()).toBe(true);
  }
  expect(createAppCalls).toBe(0);
  expect(createWorkspaceCalls).toBe(0);
  expect((await createApp(request("/api/customer/apps", JSON.stringify({ app_slug: "hello" })))).status).toBe(201);
  expect((await createWorkspace(request("/api/customer/workspaces", JSON.stringify({ app_id: appID, slug: "ada" })))).status).toBe(201);
});

test("config mutation stops oversized streams and keeps valid multibyte values", async () => {
  customerSession = { user: { id: `usr_${"b".repeat(32)}` } };
  const stream = oversizedStream(4 * (64 * 1024 + 1024));
  expect((await setConfig(request("/api/customer/config/apps/" + appID + "/variables/TEST", stream.body, "PUT"), configContext)).status).toBe(400);
  expect(stream.wasCanceled()).toBe(true);
  expect(configWrites).toHaveLength(0);

  const valid = JSON.stringify({ value: "é".repeat(32 * 1024) });
  expect((await setConfig(request("/api/customer/config/apps/" + appID + "/variables/TEST", valid, "PUT"), configContext)).status).toBe(200);
  expect(configWrites).toHaveLength(1);
  expect(configWrites[0][4]).toBe("é".repeat(32 * 1024));
});
