import { afterEach, expect, mock, test } from "bun:test";
import { generateKeyPairSync } from "node:crypto";
mock.module("server-only", () => ({}));
import { tenantAccessToken } from "../auth/tokens";
const { resolveSessionIdentity } = await import("../auth/identity");
import { ControlError } from "../domain/errors";
const { authenticatedUserID, authenticationErrorResponse } = await import("./auth");

const environment = { ...process.env };
afterEach(() => { process.env = { ...environment }; });

const source = "usr_0123456789abcdef0123456789abcdef";
const target = "usr_fedcba9876543210fedcba9876543210";
function token() {
  const { privateKey } = generateKeyPairSync("ed25519");
  process.env.CANTELOP_CONTROL_ORIGIN = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
  return tenantAccessToken(source);
}

test("an unexpired pre-merge access token resolves its canonical active subject", async () => {
  const credential = token();
  const control = { async assertActiveUser(id) { expect(id).toBe(source); return { id: target }; } };
  for (const scope of ["tenant:read", "tenant:write"]) {
    expect(await authenticatedUserID(`Bearer ${credential}`, scope, control)).toBe(target);
  }
});

test("invalid credentials never reach account resolution", async () => {
  const credential = token();
  const control = { assertActiveUser() { throw new Error("must not query"); } };
  for (const authorization of [null, "Bearer ", "Basic x", `Bearer ${credential}x`, `Bearer ${credential},x`]) {
    try { await authenticatedUserID(authorization,"tenant:read",control); throw new Error("accepted invalid credential"); }
    catch (error) { expect(authenticationErrorResponse(error).status).toBe(401); }
  }
});

for (const code of ["not_found", "user_suspended"]) {
  test(`a ${code} canonical account invalidates browser and API authentication`, async () => {
    const control = { async assertActiveUser() { throw new ControlError(code); } };
    expect(await resolveSessionIdentity(source,control)).toBeNull();
    try { await authenticatedUserID(`Bearer ${token()}`,"tenant:read",control); throw new Error("accepted inactive user"); }
    catch (error) { expect(authenticationErrorResponse(error).status).toBe(401); }
  });
}

test("database outages remain retryable without exposing internal errors", async () => {
  const control = { async assertActiveUser() { throw new Error("private database failure"); } };
  await expect(resolveSessionIdentity(source,control)).rejects.toThrow("private database failure");
  try { await authenticatedUserID(`Bearer ${token()}`,"tenant:read",control); throw new Error("accepted unavailable user"); }
  catch (error) {
    const response = authenticationErrorResponse(error);
    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({ error: "control_unavailable" });
  }
});
