import { afterEach, expect, test } from "bun:test";
import { generateKeyPairSync } from "node:crypto";

import {
  cliRefreshToken,
  publicControlOrigin,
  tenantAccessToken,
  verifyCLIRefreshToken,
  verifyTenantAccessToken,
} from "./tokens";

const userID = "usr_0123456789abcdef0123456789abcdef";
const originalWorkspace = { ...process.env };

afterEach(() => {
  process.env = { ...originalWorkspace };
});

function configure() {
  const { privateKey } = generateKeyPairSync("ed25519");
  process.env.NEXTAUTH_URL = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
}

test("mints and verifies product API access tokens", () => {
  configure();
  const compact = tenantAccessToken(userID);
  expect(verifyTenantAccessToken(compact, "tenant:read")).toBe(userID);
  expect(verifyTenantAccessToken(compact, "tenant:write")).toBe(userID);
});

test("keeps CLI refresh credentials out of the product API", () => {
  configure();
  const compact = cliRefreshToken(userID);
  expect(verifyCLIRefreshToken(compact)).toBe(userID);
  expect(() => verifyTenantAccessToken(compact, "tenant:read")).toThrow("invalid access credential");
});

test("uses the console origin as the public control origin", () => {
  configure();
  expect(publicControlOrigin()).toBe("https://console.example.com");
});

test("moving the issuer setting preserves existing access and refresh credentials", () => {
  configure();
  const access = tenantAccessToken(userID);
  const refresh = cliRefreshToken(userID);
  process.env.CANTELOP_CONTROL_ORIGIN = process.env.NEXTAUTH_URL;
  delete process.env.NEXTAUTH_URL;
  expect(publicControlOrigin()).toBe("https://console.example.com");
  expect(verifyTenantAccessToken(access, "tenant:read")).toBe(userID);
  expect(verifyCLIRefreshToken(refresh)).toBe(userID);
  process.env.NEXTAUTH_URL = "https://cantelop.com";
  expect(publicControlOrigin()).toBe("https://console.example.com");
});

test("rejects a modified credential", () => {
  configure();
  const compact = tenantAccessToken(userID);
  expect(() => verifyTenantAccessToken(`${compact.slice(0, -1)}x`, "tenant:read")).toThrow("invalid access credential");
});
