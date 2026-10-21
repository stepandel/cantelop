import { afterEach, expect, mock, test } from "bun:test";
import { generateKeyPairSync } from "node:crypto";
import { tenantAccessToken } from "@core/auth/tokens";
import { ControlError } from "@core/domain/errors";

let service;
mock.module("@core/deployment-uploads/control", () => ({ deploymentControl: () => service }));
mock.module("@core/product-control", () => ({ productControl: () => ({ assertActiveUser: async (id) => ({ id }) }) }));
const { GET } = await import("./route");
const { DELETE } = await import("./[upload_id]/route");
const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const uploadID = "upl_0123456789abcdef0123456789abcdef";
const context = { params: Promise.resolve({ id: appID, upload_id: uploadID }) };
const environment = { ...process.env };
afterEach(() => { process.env = { ...environment }; service = undefined; });

function request(method) {
  const { privateKey } = generateKeyPairSync("ed25519");
  process.env.NEXTAUTH_URL = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
  return new Request("https://console.example.com/v1/apps/" + appID + "/deployments/uploads", {
    method, headers: { Authorization: `Bearer ${tenantAccessToken(userID)}` },
  });
}

test("upload discovery and cancellation require authentication before accessing storage", async () => {
  const unauthenticated = new Request("https://console.example.com");
  expect((await GET(unauthenticated, context)).status).toBe(401);
  expect((await DELETE(unauthenticated, context)).status).toBe(401);
});

test("discovery forwards the authenticated owner and prevents caching", async () => {
  service = { async listUploads(owner, app) {
    expect([owner, app]).toEqual([userID, appID]);
    return { items: [{ id: uploadID, app_id: appID, status: "uploading" }] };
  } };
  const response = await GET(request("GET"), context);
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
  expect(await response.json()).toMatchObject({ items: [{ id: uploadID }] });
});

test("cancellation targets the owned upload and returns cleanup status", async () => {
  service = { async cancelUpload(owner, app, id) {
    expect([owner, app, id]).toEqual([userID, appID, uploadID]);
    return { id, app_id: app, status: "cancelled", cleanup_pending: true };
  } };
  const response = await DELETE(request("DELETE"), context);
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
  expect(await response.json()).toMatchObject({ status: "cancelled", cleanup_pending: true });
});

test.each([["not_found", 404], ["conflict", 409]])("cancellation maps %s to HTTP %s", async (code, status) => {
  service = { async cancelUpload() { throw new ControlError(code); } };
  const response = await DELETE(request("DELETE"), context);
  expect(response.status).toBe(status);
  expect(await response.json()).toEqual({ error: code });
});

const { POST: recover } = await import("../[release_id]/[action]/route");
const releaseID = "rel_0123456789abcdef0123456789abcdef";
test.each(["cancel", "restart"])("release %s is authenticated, owner-scoped and asynchronous", async (action) => {
  const params = { params: Promise.resolve({ id: appID, release_id: releaseID, action }) };
  expect((await recover(new Request("https://console.example.com"), params)).status).toBe(401);
  service = { async recover(owner, app, id, restart) {
    expect([owner, app, id, restart]).toEqual([userID, appID, releaseID, action === "restart"]);
    return { id, status: "cancelling" };
  } };
  const response = await recover(request("POST"), params);
  expect(response.status).toBe(202);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
});

let customerSession;
mock.module("@/lib/user-session", () => ({ getUserSession: async () => customerSession }));
const { POST: customerRecover } = await import("@/app/api/customer/apps/[id]/deployments/[release_id]/[action]/route");
test("browser recovery requires a session and same console origin", async () => {
  const params = { params: Promise.resolve({ id: appID, release_id: releaseID, action: "restart" }) };
  const makeRequest = (origin) => new Request("https://console.cantelop.dev/api/customer/recovery", {
    method: "POST", headers: { host: "console.cantelop.dev", origin },
  });
  customerSession = undefined;
  expect((await customerRecover(makeRequest("https://console.cantelop.dev"), params)).status).toBe(401);
  customerSession = { user: { id: userID } };
  expect((await customerRecover(makeRequest("https://foreign.example"), params)).status).toBe(400);
  service = { async recover(owner, app, id, restart) {
    expect([owner, app, id, restart]).toEqual([userID, appID, releaseID, true]);
    return { id, status: "cancelling" };
  } };
  expect((await customerRecover(makeRequest("https://console.cantelop.dev"), params)).status).toBe(202);
});
