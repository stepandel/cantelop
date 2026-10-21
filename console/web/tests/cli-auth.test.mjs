import { expect, mock, test } from "bun:test";
import { generateKeyPairSync, verify } from "node:crypto";

mock.module("server-only", () => ({}));

const userID = "usr_0123456789abcdef0123456789abcdef";
const activeControl = { async assertActiveUser(id) { expect(id).toBe(userID); return { id }; } };

test("exchanges a Cantelop CLI credential for a scoped tenant token", async () => {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  process.env.NEXTAUTH_URL = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
  const { cliRefreshToken } = await import("../../core/auth/tokens.ts");
  const { cliConfiguration, exchangeCLICredential } = await import("../lib/cli-auth.ts");
  expect(cliConfiguration()).toEqual({ control_url: "https://console.example.com" });
  const result = await exchangeCLICredential(cliRefreshToken(userID), activeControl);
  expect(result).toMatchObject({ token_type: "Bearer", expires_in: 60, control_url: "https://console.example.com" });
  const [header, payload, signature] = result.access_token.split(".");
  expect(verify(null, Buffer.from(`${header}.${payload}`), publicKey, Buffer.from(signature, "base64url"))).toBe(true);
  expect(JSON.parse(Buffer.from(payload, "base64url"))).toMatchObject({
    sub: userID,
    scope: ["tenant:read", "tenant:write"],
  });
});

test("rejects a modified Cantelop CLI credential", async () => {
  const { cliRefreshToken } = await import("../../core/auth/tokens.ts");
  const { exchangeCLICredential } = await import("../lib/cli-auth.ts");
  const credential = cliRefreshToken(userID);
  await expect(exchangeCLICredential(`${credential.slice(0, -1)}x`, activeControl)).rejects.toThrow("invalid access credential");
});

test("rejects a valid CLI credential after the tenant is suspended", async () => {
  const { cliRefreshToken } = await import("../../core/auth/tokens.ts");
  const { exchangeCLICredential } = await import("../lib/cli-auth.ts");
  const suspendedControl = { async assertActiveUser() { throw new Error("user_suspended"); } };
  await expect(exchangeCLICredential(cliRefreshToken(userID), suspendedControl)).rejects.toThrow("user_suspended");
});

test("a pre-merge CLI refresh credential mints an access token for the canonical user", async () => {
  const { cliRefreshToken, verifyTenantAccessToken } = await import("../../core/auth/tokens.ts");
  const { exchangeCLICredential } = await import("../lib/cli-auth.ts");
  const canonical = "usr_fedcba9876543210fedcba9876543210";
  const control = { async assertActiveUser(id) { expect(id).toBe(userID); return { id: canonical }; } };
  const result = await exchangeCLICredential(cliRefreshToken(userID), control);
  expect(verifyTenantAccessToken(result.access_token, "tenant:read")).toBe(canonical);
});
