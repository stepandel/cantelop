import { afterEach, expect, mock, test } from "bun:test";
import { generateKeyPairSync } from "node:crypto";
import { tenantAccessToken } from "@core/auth/tokens";
import { ControlError } from "@core/domain/errors";

let getApp;
let routes;
mock.module("@core/product-control", () => ({ productControl: () => ({ assertActiveUser: async (id) => ({ id }), getApp: (...args) => getApp(...args) }) }));
mock.module("@core/api-routes/control", () => ({ appRoutes: (...args) => routes(...args), appRouteDetail: () => { throw new Error("not this test"); }, relayToApp: () => { throw new Error("not this test"); } }));
const { GET } = await import("./route");
const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const context = { params: Promise.resolve({ id: appID }) };
const environment = { ...process.env };
afterEach(() => { process.env = { ...environment }; getApp = undefined; routes = undefined; });

function request() {
  const { privateKey } = generateKeyPairSync("ed25519");
  process.env.NEXTAUTH_URL = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
  return new Request(`https://console.example.com/v1/apps/${appID}/routes`, {
    headers: { Authorization: `Bearer ${tenantAccessToken(userID)}` },
  });
}

test("route listing requires authentication before reading the App", async () => {
  getApp = () => { throw new Error("read before authentication"); };
  expect((await GET(new Request("https://console.example.com"), context)).status).toBe(401);
});

test("returns the owner's route overview with the App's public origin, uncached", async () => {
  getApp = async (owner, app) => {
    expect([owner, app]).toEqual([userID, appID]);
    return { id: appID, slug: "vera", hostname: "vera.cantelop.dev" };
  };
  routes = async (owner, app) => {
    expect([owner, app]).toEqual([userID, appID]);
    return { declaration: "declared", routes: [{ method: "GET", path: "/health" }], observed_only: [] };
  };
  const response = await GET(request(), context);
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
  expect(await response.json()).toEqual({
    origin: "https://vera.cantelop.dev",
    declaration: "declared",
    routes: [{ method: "GET", path: "/health" }],
    observed_only: [],
  });
});

test("an App the caller does not own is not found", async () => {
  getApp = async () => { throw new ControlError("not_found"); };
  routes = async () => { throw new Error("read routes of a foreign App"); };
  expect((await GET(request(), context)).status).toBe(404);
});
