import type { ComputeTenantResolver } from "../db/compute-tenant";
import { SessionRuntimeProviderError, type SessionRuntimeMaterialization, type SessionRuntimeMaterializationCommand, type SessionRuntimeProvider } from "./provider";

const userIDPattern = /^usr_[0-9a-f]{32}$/;
const appIDPattern = /^app_[0-9a-f]{32}$/;
const operationIDPattern = /^op_([0-9a-f]{32})$/;
const buildIDPattern = /^sha256:[0-9a-f]{64}$/;
const errorCodePattern = /^[a-z][a-z0-9_]{0,63}$/;
const tokenPattern = /^[0-9a-f]{64}$/;
const generationPattern = /^act_[0-9a-f]{32}$/;

interface FireFuseProviderConfig {
  ingressOrigin: string;
  token: string;
}

export class FireFuseSessionRuntimeProvider implements SessionRuntimeProvider {
  private readonly ingressOrigin: string;

  constructor(
    private readonly config: FireFuseProviderConfig,
    private readonly request: typeof fetch = fetch,
    private readonly tenantForApp: ComputeTenantResolver = async (_appID, ownerID) => ownerID,
  ) {
    this.ingressOrigin = validateOrigin(config.ingressOrigin);
    if (!tokenPattern.test(config.token)) throw new TypeError("invalid Fire Fuse control token");
  }

  async cancel(ownerUserID: string, appID: string, commandID: string): Promise<void> {
    if (!userIDPattern.test(ownerUserID) || !appIDPattern.test(appID) || !operationIDPattern.test(commandID)) {
      throw new TypeError("invalid materialization cancellation");
    }
    const response = await this.request(`${this.ingressOrigin}/v1/internal/users/${await this.tenantForApp(appID, ownerUserID)}/apps/${appID}/prepared-builds/${commandID}`, {
      method: "DELETE", redirect: "error", headers: { authorization: `Bearer ${this.config.token}` },
      signal: AbortSignal.timeout(30_000),
    });
    // A 404 is not success: an older ingress may not implement cancellation.
    if (response.status !== 200) throw new SessionRuntimeProviderError(true);
    const document = await response.json();
    if (!plainObject(document) || document.id !== `bop_${operationIDPattern.exec(commandID)![1]}` || document.app_id !== appID ||
        (document.status !== "failed" && document.status !== "succeeded")) throw new SessionRuntimeProviderError(true);
  }

  async selectBuild(ownerUserID: string, appID: string, buildID: string | null): Promise<void> {
    if (!userIDPattern.test(ownerUserID) || !appIDPattern.test(appID) || (buildID !== null && !buildIDPattern.test(buildID))) {
      throw new TypeError("invalid active Build selection");
    }
    const tenant = await this.tenantForApp(appID, ownerUserID);
    let response: Response;
    try {
      response = await this.request(`${this.ingressOrigin}/v1/internal/users/${tenant}/apps/${appID}/active-build`, {
        method: "PUT", redirect: "error",
        headers: { authorization: `Bearer ${this.config.token}`, "content-type": "application/json" },
        body: JSON.stringify({ build_id: buildID }),
        signal: AbortSignal.timeout(30_000),
      });
    } catch {
      throw new SessionRuntimeProviderError(true, undefined, "provider_unreachable");
    }
    if (response.status !== 200) {
      throw new SessionRuntimeProviderError(true, undefined, `provider_http_${response.status}`);
    }
    let result: unknown;
    try { result = await response.json(); }
    catch { throw new SessionRuntimeProviderError(true, undefined, "provider_invalid_response"); }
    if (!plainObject(result) || result.app_id !== appID || result.build_id !== buildID) {
      throw new SessionRuntimeProviderError(true, undefined, "provider_invalid_response");
    }
  }

  async activeBuild(ownerUserID: string, appID: string): Promise<string | null> {
    const result = await this.readControl(ownerUserID, appID, "active-build");
    if (!plainObject(result) || result.app_id !== appID ||
        (result.build_id !== null && (typeof result.build_id !== "string" || !buildIDPattern.test(result.build_id)))) {
      throw new SessionRuntimeProviderError(true, undefined, "provider_invalid_response");
    }
    return result.build_id;
  }

  async retainsBuild(ownerUserID: string, appID: string, buildID: string): Promise<boolean> {
    if (!userIDPattern.test(ownerUserID) || !appIDPattern.test(appID) || !buildIDPattern.test(buildID)) {
      throw new TypeError("invalid retained Build query");
    }
    const tenant = await this.tenantForApp(appID, ownerUserID);
    let response: Response;
    try {
      response = await this.request(`${this.ingressOrigin}/v1/internal/users/${tenant}/apps/${appID}/builds/${buildID}`, {
        method: "GET", redirect: "error", headers: { authorization: `Bearer ${this.config.token}` },
        signal: AbortSignal.timeout(30_000),
      });
    } catch { throw new SessionRuntimeProviderError(true, undefined, "provider_unreachable"); }
    let document: unknown;
    try { document = await response.json(); } catch { document = undefined; }
    // Only an explicit answer counts: a bare 404 may be an ingress without this endpoint.
    if (response.status === 404 && plainObject(document) && plainObject(document.error) && document.error.code === "build_not_found") return false;
    if (response.status === 200 && plainObject(document) && document.app_id === appID && document.build_id === buildID) return true;
    throw new SessionRuntimeProviderError(true, undefined, `provider_http_${response.status}`);
  }

  async releaseAdmission(ownerUserID: string, appID: string): Promise<{ generation: string; closed: boolean }> {
    const result = await this.readControl(ownerUserID, appID, "release-admission");
    if (!plainObject(result) || typeof result.generation !== "string" ||
        (result.generation !== "" && !generationPattern.test(result.generation)) ||
        typeof result.closed !== "boolean") throw new SessionRuntimeProviderError(true, undefined, "provider_invalid_response");
    return { generation: result.generation, closed: result.closed };
  }

  async setReleaseAdmission(ownerUserID: string, appID: string, generation: string, closed: boolean): Promise<void> {
    if (!generationPattern.test(generation)) throw new TypeError("invalid release admission generation");
    const result = await this.readControl(ownerUserID, appID, "release-admission", { generation, closed });
    if (!plainObject(result) || result.generation !== generation || result.closed !== closed) {
      throw new SessionRuntimeProviderError(true, undefined, "provider_invalid_response");
    }
  }

  private async readControl(ownerUserID: string, appID: string, endpoint: string,
    command?: Record<string, unknown>): Promise<unknown> {
    if (!userIDPattern.test(ownerUserID) || !appIDPattern.test(appID)) throw new TypeError("invalid App control identity");
    const tenant = await this.tenantForApp(appID, ownerUserID);
    let response: Response;
    try {
      response = await this.request(`${this.ingressOrigin}/v1/internal/users/${tenant}/apps/${appID}/${endpoint}`, {
        method: command ? "PUT" : "GET", redirect: "error",
        headers: { authorization: `Bearer ${this.config.token}`, ...(command ? { "content-type": "application/json" } : {}) },
        ...(command ? { body: JSON.stringify(command) } : {}),
        signal: AbortSignal.timeout(30_000),
      });
    } catch { throw new SessionRuntimeProviderError(true, undefined, "provider_unreachable"); }
    if (response.status !== 200) throw new SessionRuntimeProviderError(true, undefined, `provider_http_${response.status}`);
    try { return await response.json(); }
    catch { throw new SessionRuntimeProviderError(true, undefined, "provider_invalid_response"); }
  }

  async materialize(command: SessionRuntimeMaterializationCommand): Promise<SessionRuntimeMaterialization> {
    validateCommand(command);
    // Resolved before the request: a control-database failure here is not the
    // compute provider being unreachable, and must not be reported as one.
    const tenant = await this.tenantForApp(command.appID, command.ownerUserID);
    let response: Response;
    try {
      response = await this.request(
        `${this.ingressOrigin}/v1/internal/users/${tenant}/apps/${command.appID}/prepared-builds`,
        {
          method: "POST",
          redirect: "error",
          headers: {
            authorization: `Bearer ${this.config.token}`,
            "content-type": "application/json",
          },
          body: JSON.stringify({
            command_id: command.commandID,
            image_url: command.imageURL,
            manifest: command.manifest,
          }),
          signal: AbortSignal.timeout(30 * 60_000),
        },
      );
    } catch {
      throw new SessionRuntimeProviderError(true, undefined, "provider_unreachable");
    }
    if (response.status === 429) {
      let error: unknown;
      try { error = await response.json(); } catch { /* Ordinary rate limits remain retryable. */ }
      if (plainObject(error) && plainObject(error.error) && error.error.code === "build_retention_quota") {
        throw new SessionRuntimeProviderError(false, "build_retention_quota");
      }
      throw new SessionRuntimeProviderError(true, undefined, "provider_http_429");
    }
    if (response.status === 507) {
      // Disk capacity needs operator action or collection, not a quick retry.
      throw new SessionRuntimeProviderError(false, "host_disk_capacity", "provider_http_507");
    }
    if (response.status !== 200 && response.status !== 202 && response.status !== 502 && response.status !== 503) {
      // A release can be queued immediately after App creation while the
      // independent registration operation is still converging on Fire Fuse.
      throw new SessionRuntimeProviderError(response.status === 404 || response.status === 409 || response.status === 429 || response.status >= 500, undefined, `provider_http_${response.status}`);
    }
    let document: unknown;
    try {
      document = await response.json();
    } catch {
      throw new SessionRuntimeProviderError(true, undefined, response.status >= 400 ? `provider_http_${response.status}` : "provider_invalid_response");
    }
    try { return validateResponse(document, command); }
    catch { throw new SessionRuntimeProviderError(true, undefined, response.status >= 400 ? `provider_http_${response.status}` : "provider_invalid_response"); }
  }
}

export function configuredFireFuseSessionRuntimeProvider(
  environment: NodeJS.ProcessEnv = process.env,
  request: typeof fetch = fetch,
  tenantForApp?: ComputeTenantResolver,
): FireFuseSessionRuntimeProvider {
  const ingressOrigin = required(environment, "CANTELOP_COMPUTE_INGRESS_URL");
  const token = required(environment, "CANTELOP_COMPUTE_CONTROL_TOKEN");
  return new FireFuseSessionRuntimeProvider({ ingressOrigin, token }, request, tenantForApp);
}

function validateCommand(command: SessionRuntimeMaterializationCommand): void {
  if (!operationIDPattern.test(command.commandID) || !userIDPattern.test(command.ownerUserID) ||
      !appIDPattern.test(command.appID) || !buildIDPattern.test(command.manifest.build_id) ||
      !/^[0-9a-f]{64}$/.test(command.manifest.rootfs_sha256) ||
      command.manifest.image_key !== `prepared-images/sha256/${command.manifest.rootfs_sha256}.ext4` ||
      !Number.isSafeInteger(command.manifest.rootfs_size_bytes) || command.manifest.rootfs_size_bytes < 1 ||
      command.manifest.rootfs_size_bytes > 52 * 1024 ** 3) {
    throw new TypeError("invalid Session runtime materialization command");
  }
  const source = new URL(command.imageURL);
  if (source.protocol !== "https:" || source.username || source.password || source.hash || !source.search) {
    throw new TypeError("invalid Session runtime materialization source URL");
  }
}

function validateResponse(document: unknown, command: SessionRuntimeMaterializationCommand): SessionRuntimeMaterialization {
  if (!plainObject(document)) throw new SessionRuntimeProviderError(true);
  const expectedOperationID = `bop_${operationIDPattern.exec(command.commandID)![1]}`;
  const status = document.status;
  if (document.id !== expectedOperationID || document.app_id !== command.appID ||
      (status !== "receiving" && status !== "succeeded" && status !== "failed")) {
    throw new SessionRuntimeProviderError(true);
  }
  const result: SessionRuntimeMaterialization = { operationID: expectedOperationID, status };
  if (status === "succeeded") {
    if (typeof document.build_id !== "string" || document.build_id !== command.manifest.build_id ||
        !Number.isInteger(document.build_version) || (document.build_version as number) < 1) throw new SessionRuntimeProviderError(true);
    result.buildID = document.build_id;
    result.buildVersion = document.build_version as number;
  } else if (status === "failed") {
    if (typeof document.error_code !== "string" || !errorCodePattern.test(document.error_code)) {
      throw new SessionRuntimeProviderError(true);
    }
    result.errorCode = document.error_code;
  } else if (document.build_id !== undefined || document.build_version !== undefined || document.error_code !== undefined) {
    throw new SessionRuntimeProviderError(true);
  }
  return result;
}

function validateOrigin(raw: string): string {
  const endpoint = new URL(raw);
  const local = endpoint.protocol === "http:" && ["127.0.0.1", "::1", "localhost"].includes(endpoint.hostname);
  const tunnel = endpoint.origin === "http://10.77.0.1:8084";
  if ((!local && !tunnel && endpoint.protocol !== "https:") || endpoint.username || endpoint.password || endpoint.pathname !== "/" ||
      endpoint.search || endpoint.hash) throw new TypeError("invalid Fire Fuse ingress origin");
  return endpoint.origin;
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
