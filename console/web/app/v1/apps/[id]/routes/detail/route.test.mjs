import { afterEach, expect, mock, test } from "bun:test";
import { generateKeyPairSync } from "node:crypto";
import { NextRequest } from "next/server";
import { tenantAccessToken } from "@core/auth/tokens";

let detail;
mock.module("@core/api-routes/control", () => ({ appRouteDetail: (...args) => detail(...args), appRoutes: () => { throw new Error("not this test"); }, relayToApp: () => { throw new Error("not this test"); } }));
mock.module("@core/product-control", () => ({ productControl: () => ({ assertActiveUser: async (id) => ({ id }) }) }));
const { GET } = await import("./route");
const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const context = { params: Promise.resolve({ id: appID }) };
const environment = { ...process.env };
afterEach(() => { process.env = { ...environment }; detail = undefined; });

function request(query) {
  const { privateKey } = generateKeyPairSync("ed25519");
  process.env.NEXTAUTH_URL = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
  return new NextRequest(`https://console.example.com/v1/apps/${appID}/routes/detail?${query}`, {
    headers: { Authorization: `Bearer ${tenantAccessToken(userID)}` },
  });
}

test("route detail requires authentication", async () => {
  detail = () => { throw new Error("read before authentication"); };
  expect((await GET(new NextRequest("https://console.example.com/x?method=GET&path=/"), context)).status).toBe(401);
});

test("names the route from the query and forwards the owner", async () => {
  detail = async (owner, app, method, path) => {
    expect([owner, app, method, path]).toEqual([userID, appID, "POST", "/chat"]);
    return { method, path, declared: true };
  };
  const response = await GET(request("method=POST&path=%2Fchat"), context);
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
  expect(await response.json()).toEqual({ method: "POST", path: "/chat", declared: true });
});

test("rejects a malformed route before reading anything", async () => {
  detail = () => { throw new Error("read with an invalid route"); };
  for (const query of ["", "method=post&path=/chat", "method=POST&path=chat", "method=POST", "path=/chat", "method=POST&path=/a%0Ab"]) {
    expect((await GET(request(query), context)).status).toBe(400);
  }
});
