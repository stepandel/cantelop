import { createPrivateKey, randomUUID, sign } from "node:crypto";

const subject = process.argv[2]?.trim();
const issuer = (process.env.CANTELOP_CONTROL_ORIGIN ?? process.env.NEXTAUTH_URL)?.trim();
const encodedKey = process.env.CANTELOP_CONTROL_PRIVATE_KEY?.trim();

if (!subject || !/^usr_[0-9a-f]{32}$/.test(subject)) {
  throw new Error("usage: bun run mint-qualification-token -- usr_<tenant-user-id>");
}
if (!issuer || !/^https:\/\/[^/?#]+$/.test(issuer)) {
  throw new Error("CANTELOP_CONTROL_ORIGIN must be an HTTPS origin");
}
if (!encodedKey) {
  throw new Error("CANTELOP_CONTROL_PRIVATE_KEY is required");
}

const encode = (value) => Buffer.from(value).toString("base64url");
const now = Math.floor(Date.now() / 1000);
const header = encode(JSON.stringify({ alg: "EdDSA", typ: "JWT" }));
const payload = encode(JSON.stringify({
  iss: issuer,
  aud: "cantelop-control",
  sub: subject,
  scope: ["tenant:read"],
  iat: now,
  exp: now + 60,
  jti: randomUUID(),
}));
const signingInput = `${header}.${payload}`;
const key = createPrivateKey({
  key: Buffer.from(encodedKey, "base64url"),
  format: "der",
  type: "pkcs8",
});
process.stdout.write(`${signingInput}.${sign(null, Buffer.from(signingInput), key).toString("base64url")}\n`);
