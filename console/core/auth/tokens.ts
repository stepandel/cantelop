import { createPrivateKey, createPublicKey, randomUUID, sign, verify } from "node:crypto";

import { assertResourceID } from "../domain/ids";

export type TenantScope = "tenant:read" | "tenant:write";

const accessLifetimeSeconds = 60;
const refreshLifetimeSeconds = 30 * 24 * 60 * 60;

function required(name: "CANTELOP_CONTROL_PRIVATE_KEY" | "CANTELOP_CONTROL_ORIGIN") {
  const value = (process.env[name] ?? (name === "CANTELOP_CONTROL_ORIGIN" ? process.env.NEXTAUTH_URL : undefined))?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

export function publicControlOrigin(): string {
  const raw = new URL(required("CANTELOP_CONTROL_ORIGIN"));
  if (raw.username || raw.password || raw.search || raw.hash || (raw.pathname !== "/" && raw.pathname !== "")) {
    throw new Error("CANTELOP_CONTROL_ORIGIN must be an origin");
  }
  const localDevelopment = raw.protocol === "http:" && ["127.0.0.1", "::1", "localhost"].includes(raw.hostname);
  if (raw.protocol !== "https:" && !localDevelopment) throw new Error("CANTELOP_CONTROL_ORIGIN must use HTTPS");
  return raw.origin;
}

export function tenantAccessToken(userID: string): string {
  assertResourceID("user", userID);
  return signedToken(userID, ["tenant:read", "tenant:write"], "cantelop-control", accessLifetimeSeconds);
}

export function cliRefreshToken(userID: string): string {
  assertResourceID("user", userID);
  return signedToken(userID, ["tenant:refresh"], "cantelop-cli", refreshLifetimeSeconds);
}

export function verifyCLIRefreshToken(compact: string): string {
  const claims = verifiedClaims(compact, "cantelop-cli", ["tenant:refresh"], refreshLifetimeSeconds);
  assertResourceID("user", claims.sub);
  return claims.sub;
}

export function verifyTenantAccessToken(compact: string, requiredScope: TenantScope): string {
  const claims = verifiedClaims(compact, "cantelop-control", ["tenant:read", "tenant:write"], accessLifetimeSeconds);
  if (!claims.scope.includes(requiredScope)) throw new Error("invalid access credential");
  assertResourceID("user", claims.sub);
  return claims.sub;
}

function signedToken(subject: string, scopes: string[], audience: string, lifetimeSeconds: number): string {
  const now = Math.floor(Date.now() / 1000);
  const header = base64url(JSON.stringify({ alg: "EdDSA", typ: "JWT" }));
  const payload = base64url(JSON.stringify({
    iss: publicControlOrigin(),
    aud: audience,
    sub: subject,
    scope: scopes,
    iat: now,
    exp: now + lifetimeSeconds,
    jti: randomUUID(),
  }));
  const signingInput = `${header}.${payload}`;
  return `${signingInput}.${sign(null, Buffer.from(signingInput), privateKey()).toString("base64url")}`;
}

function verifiedClaims(compact: string, audience: string, exactScopes: string[], lifetimeSeconds: number) {
  const parts = compact.split(".");
  if (parts.length !== 3 || parts.some((part) => !/^[A-Za-z0-9_-]+$/.test(part))) throw new Error("invalid access credential");
  const decoded = parts.map((part) => Buffer.from(part, "base64url"));
  if (decoded.some((part, index) => part.toString("base64url") !== parts[index])) throw new Error("invalid access credential");
  let header: Record<string, unknown>;
  let claims: Record<string, unknown>;
  try {
    header = JSON.parse(decoded[0].toString("utf8")) as Record<string, unknown>;
    claims = JSON.parse(decoded[1].toString("utf8")) as Record<string, unknown>;
  } catch {
    throw new Error("invalid access credential");
  }
  const now = Math.floor(Date.now() / 1000);
  if (header.alg !== "EdDSA" || header.typ !== "JWT" || claims.iss !== publicControlOrigin() ||
      claims.aud !== audience || typeof claims.sub !== "string" || !Array.isArray(claims.scope) ||
      claims.scope.length !== exactScopes.length || claims.scope.some((scope, index) => scope !== exactScopes[index]) ||
      typeof claims.iat !== "number" || !Number.isInteger(claims.iat) || typeof claims.exp !== "number" ||
      !Number.isInteger(claims.exp) || claims.iat > now + 30 || claims.exp <= now ||
      claims.exp - claims.iat !== lifetimeSeconds || typeof claims.jti !== "string") {
    throw new Error("invalid access credential");
  }
  if (!verify(null, Buffer.from(`${parts[0]}.${parts[1]}`), createPublicKey(privateKey()), decoded[2])) {
    throw new Error("invalid access credential");
  }
  return claims as { sub: string; scope: string[] };
}

function privateKey() {
  return createPrivateKey({
    key: Buffer.from(required("CANTELOP_CONTROL_PRIVATE_KEY"), "base64url"),
    format: "der",
    type: "pkcs8",
  });
}

function base64url(document: string | Buffer) {
  return Buffer.from(document).toString("base64url");
}
