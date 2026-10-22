import { createHmac, timingSafeEqual } from "node:crypto";
import type { SQLClient } from "../db/database";
export interface SandboxDatabaseIdentity { sandboxId: string; appId: string; workspaceId: string }
type VerificationCode = "verification_configuration_invalid" | "verification_timeout" | "verification_unavailable" | "verification_http_error" | "verification_response_invalid" | "inactive_sandbox";
export class SandboxVerificationError extends Error {
  constructor(readonly code: VerificationCode, readonly status?: number) {
    super(code === "inactive_sandbox" ? "inactive sandbox" : "database credential service unavailable");
    this.name = "SandboxVerificationError";
  }
}
export function verifyDatabaseCapability(authorization: string | null, key: string): SandboxDatabaseIdentity {
  if (!/^[0-9a-f]{64}$/.test(key) || !authorization?.startsWith("Bearer ") || authorization.length > 2048) throw new Error("invalid database capability");
  const parts = authorization.slice(7).split(".");
  if (parts.length !== 2 || !parts.every(p => /^[A-Za-z0-9_-]+$/.test(p))) throw new Error("invalid database capability");
  const expected = createHmac("sha256", Buffer.from(key, "hex")).update(`cantelop-workspace-database-v1\0${parts[0]}`).digest();
  const mac = Buffer.from(parts[1]!, "base64url");
  if (mac.length !== expected.length || !timingSafeEqual(mac, expected)) throw new Error("invalid database capability");
  const value = JSON.parse(Buffer.from(parts[0]!, "base64url").toString());
  if (!value || Object.keys(value).sort().join(",") !== "appId,sandboxId,workspaceId" ||
      !/^sbx-[0-9a-f]{32}$/.test(value.sandboxId) || !/^app_[0-9a-f]{32}$/.test(value.appId) || !/^wsp_[0-9a-f]{32}$/.test(value.workspaceId)) throw new Error("invalid database capability");
  return value;
}
export async function verifyAssignedSandbox(database: SQLClient, identity: SandboxDatabaseIdentity,
  environment: NodeJS.ProcessEnv = process.env, request: typeof fetch = fetch): Promise<void> {
  const result = await database.query<{ compute_pool: string }>(
    "SELECT compute_pool FROM cantelop_control.apps WHERE id=$1 AND status<>'archived'", [identity.appId]);
  const pool = result.rows[0]?.compute_pool;
  if (pool === undefined) throw new SandboxVerificationError("inactive_sandbox");
  if (pool !== "standard" && pool !== "managed") throw new SandboxVerificationError("verification_configuration_invalid");
  await verifyActiveSandbox(identity, pool, environment, request);
}
export async function verifyActiveSandbox(identity: SandboxDatabaseIdentity, pool: "standard" | "managed", environment: NodeJS.ProcessEnv = process.env, request: typeof fetch = fetch): Promise<void> {
  if (environment.FIRE_FUSE_OBSERVER_URL) {
    await verifyObservedSandbox(identity, pool, environment, request);
    return;
  }
  const rawOrigin = pool === "managed" ? environment.CANTELOP_MANAGED_COMPUTE_INGRESS_URL : environment.CANTELOP_COMPUTE_INGRESS_URL;
  const token = pool === "managed" ? environment.CANTELOP_MANAGED_COMPUTE_CONTROL_TOKEN : environment.CANTELOP_COMPUTE_CONTROL_TOKEN;
  if (!rawOrigin || !token || !/^[0-9a-f]{64}$/.test(token)) throw new SandboxVerificationError("verification_configuration_invalid");
  const origin = verificationOrigin(rawOrigin, [pool === "managed" ? "http://10.77.0.1:8184" : "http://10.77.0.1:8084"]);
  await verifyAssignment(`${origin}/v1/internal/database-access/${identity.sandboxId}`, token, identity, request);
}

async function verifyObservedSandbox(identity: SandboxDatabaseIdentity, pool: "standard" | "managed", environment: NodeJS.ProcessEnv, request: typeof fetch): Promise<void> {
  const token = environment.FIRE_FUSE_STATUS_TOKEN;
  if (!token || token.length < 32 || token.length > 512 || token.trim() !== token) throw new SandboxVerificationError("verification_configuration_invalid");
  const origin = verificationOrigin(environment.FIRE_FUSE_OBSERVER_URL!);
  // Read one persisted assignment on every request. Credential renewal must not
  // depend on transferring the dashboard's entire historical inventory.
  await verifyAssignment(`${origin}${pool === "managed" ? "/managed" : ""}/sandboxes/${identity.sandboxId}/database-access`, token, identity, request);
}

function verificationOrigin(raw: string, allowedOrigins: string[] = []): string {
  let origin: URL;
  try { origin = new URL(raw); }
  catch { throw new SandboxVerificationError("verification_configuration_invalid"); }
  const local = origin.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(origin.hostname);
  if ((!local && !allowedOrigins.includes(origin.origin) && origin.protocol !== "https:") || origin.pathname !== "/" || origin.username || origin.password || origin.search || origin.hash) throw new SandboxVerificationError("verification_configuration_invalid");
  return origin.origin;
}

async function verifyAssignment(endpoint: string, token: string, identity: SandboxDatabaseIdentity, request: typeof fetch): Promise<void> {
  const signal = AbortSignal.timeout(5000);
  try {
    const response = await request(endpoint, {
      redirect: "error", cache: "no-store", headers: { Authorization: `Bearer ${token}` }, signal,
    });
    if (!response.ok) {
      // Only a JSON not_found denial is an inactive Sandbox. A bare 404 means the
      // proxy or observer does not serve this route.
      if (response.status === 404 && (await response.json().catch(() => null))?.error?.code === "not_found") throw new SandboxVerificationError("inactive_sandbox", 404);
      await response.body?.cancel().catch(() => {});
      throw new SandboxVerificationError("verification_http_error", response.status);
    }
    let value;
    try { value = await response.json(); }
    catch (error) {
      if (signal.aborted) throw error;
      throw new SandboxVerificationError("verification_response_invalid");
    }
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new SandboxVerificationError("verification_response_invalid");
    if (value.sandboxId !== identity.sandboxId || value.workspaceId !== identity.workspaceId || value.appId !== identity.appId || value.active !== true) throw new SandboxVerificationError("inactive_sandbox");
  } catch (error) {
    if (error instanceof SandboxVerificationError) throw error;
    throw new SandboxVerificationError(signal.aborted ? "verification_timeout" : "verification_unavailable");
  }
}
