import { expect, test } from "bun:test";
import { generateKeyPairSync, verify } from "node:crypto";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

test.each(["NEXTAUTH_URL", "CANTELOP_CONTROL_ORIGIN"])("mints a short-lived tenant-read qualification token using %s", (issuerVariable) => {
  const { privateKey, publicKey } = generateKeyPairSync("ed25519");
  const testDirectory = dirname(fileURLToPath(import.meta.url));
  const result = spawnSync(
    process.execPath,
    [resolve(testDirectory, "../../scripts/mint-qualification-token.mjs"), "usr_0123456789abcdef0123456789abcdef"],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        NEXTAUTH_URL: "",
        CANTELOP_CONTROL_ORIGIN: undefined,
        [issuerVariable]: "https://console.example.com",
        CANTELOP_CONTROL_PRIVATE_KEY: privateKey
          .export({ format: "der", type: "pkcs8" })
          .toString("base64url"),
      },
    },
  );

  expect(result.status).toBe(0);
  const compact = result.stdout.trim();
  const [header, payload, signature] = compact.split(".");
  expect(verify(
    null,
    Buffer.from(`${header}.${payload}`),
    publicKey,
    Buffer.from(signature, "base64url"),
  )).toBe(true);
  const claims = JSON.parse(Buffer.from(payload, "base64url"));
  expect(claims).toMatchObject({
    iss: "https://console.example.com",
    aud: "cantelop-control",
    sub: "usr_0123456789abcdef0123456789abcdef",
    scope: ["tenant:read"],
  });
  expect(claims.exp - claims.iat).toBe(60);
});
