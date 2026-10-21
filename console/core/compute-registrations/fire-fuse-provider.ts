import type { ComputeTenantResolver } from "../db/compute-tenant";
import {
  ComputeRegistrationProviderError,
  type ComputeAppRegistrationCommand,
  type ComputeAppArchiveCommand,
  type ComputeWorkspaceRegistrationCommand,
  type ComputeWorkspaceArchiveCommand,
  type ComputeWorkspacePurgeCommand,
  type ComputeConfigurationCommand,
  type ComputeRegistrationProvider,
} from "./provider";

const resourcePatterns = {
  user: /^usr_[0-9a-f]{32}$/,
  app: /^app_[0-9a-f]{32}$/,
  workspace: /^wsp_[0-9a-f]{32}$/,
};
const slugPattern = /^(?!.*--)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const tokenPattern = /^[0-9a-f]{64}$/;

export class FireFuseComputeRegistrationProvider implements ComputeRegistrationProvider {
  private readonly ingressOrigin: string;

  constructor(
    config: { ingressOrigin: string; token: string },
    private readonly request: typeof fetch = fetch,
    private readonly tenantForApp: ComputeTenantResolver = async (_appID, ownerID) => ownerID,
  ) {
    this.ingressOrigin = validateOrigin(config.ingressOrigin);
    if (!tokenPattern.test(config.token)) throw new TypeError("invalid Fire Fuse control token");
    this.token = config.token;
  }

  private readonly token: string;

  async registerApp(command: ComputeAppRegistrationCommand): Promise<void> {
    validateCommand(command);
    // Resolved before the request: a control-database failure here is not the
    // compute provider rejecting the registration, and must not look like one.
    const tenant = await this.tenantForApp(command.appID, command.ownerUserID);
    let response: Response;
    try {
      response = await this.request(`${this.ingressOrigin}/v1/internal/users/${tenant}/apps/${command.appID}`, {
        method: "PUT",
        redirect: "error",
        headers: { authorization: `Bearer ${this.token}`, "content-type": "application/json" },
        body: JSON.stringify({
          slug: command.appSlug,
 ...(command.managed?{managed:true}:{}),
          session_runtime: {
            runtime: { internal_port: command.internalPort, profile: "default" },
          },
        }),
        signal: AbortSignal.timeout(30_000),
      });
    } catch {
      throw new ComputeRegistrationProviderError(true);
    }
    if (response.status !== 200 && response.status !== 201) {
      throw new ComputeRegistrationProviderError(response.status === 429 || response.status >= 500);
    }
    let document: unknown;
    try {
      document = await response.json();
    } catch {
      throw new ComputeRegistrationProviderError(true);
    }
    if (!plainObject(document) || document.app_id !== command.appID || document.status !== "registered" ||
        Object.keys(document).sort().join(",") !== "app_id,status") {
      throw new ComputeRegistrationProviderError(true);
    }
  }

  async registerWorkspace(command: ComputeWorkspaceRegistrationCommand): Promise<void> {
    validateWorkspaceCommand(command);
    const response = await this.send(
      "PUT",
      `/v1/internal/users/${command.registration?.computeTenantID ?? await this.tenantForApp(command.appID, command.ownerUserID)}/apps/${command.appID}/workspaces/${command.workspaceID}`,
      { slug: command.workspaceSlug },
    );
    await validateDocument(response, { workspace_id: command.workspaceID, status: "registered" });
  }

  async archiveApp(command: ComputeAppArchiveCommand): Promise<void> {
    validateArchiveCommand(command);
    const response = await this.send(
      "DELETE",
      `/v1/internal/users/${await this.tenantForApp(command.appID, command.ownerUserID)}/apps/${command.appID}`,
    );
    await validateDocument(response, { app_id: command.appID, status: "archived" });
  }

  async archiveWorkspace(command: ComputeWorkspaceArchiveCommand): Promise<void> {
    validateWorkspaceArchiveCommand(command);
    const response = await this.send(
      "DELETE",
      `/v1/internal/users/${await this.tenantForApp(command.appID, command.ownerUserID)}/apps/${command.appID}/workspaces/${command.workspaceID}`,
    );
    await validateDocument(response, { workspace_id: command.workspaceID, status: "archived" });
  }

  async purgeWorkspace(command: ComputeWorkspacePurgeCommand): Promise<void> {
    validateWorkspaceArchiveCommand(command);
    const response = await this.send(
      "DELETE",
      `/v1/internal/users/${await this.tenantForApp(command.appID, command.ownerUserID)}/apps/${command.appID}/workspaces/${command.workspaceID}/purge`,
    );
    await validateDocument(response, { workspace_id: command.workspaceID, status: "purged" });
  }

  async replaceConfiguration(command: ComputeConfigurationCommand): Promise<void> {
    validateConfigurationCommand(command);
    const response = await this.send(
      "PUT",
      `/v1/internal/users/${await this.tenantForApp(command.appID, command.ownerUserID)}/apps/${command.appID}/configuration`,
      { revision: command.revision, values: command.values },
    );
    await validateDocument(response, { app_id: command.appID, revision: command.revision, status: "applied" });
  }

  private async send(method: "PUT" | "DELETE", path: string, body?: Record<string, unknown>): Promise<Response> {
    let response: Response;
    try {
      response = await this.request(`${this.ingressOrigin}${path}`, {
        method,
        redirect: "error",
        headers: { authorization: `Bearer ${this.token}`, ...(body ? { "content-type": "application/json" } : {}) },
        ...(body ? { body: JSON.stringify(body) } : {}),
        signal: AbortSignal.timeout(30_000),
      });
    } catch {
      throw new ComputeRegistrationProviderError(true);
    }
    if (response.status !== 200 && response.status !== 201) {
      throw new ComputeRegistrationProviderError(response.status === 404 || response.status === 409 || response.status === 429 || response.status >= 500);
    }
    return response;
  }
}

export function configuredFireFuseComputeRegistrationProvider(
  environment: NodeJS.ProcessEnv = process.env,
  request: typeof fetch = fetch,
  tenantForApp?: ComputeTenantResolver,
): FireFuseComputeRegistrationProvider {
  return new FireFuseComputeRegistrationProvider({
    ingressOrigin: required(environment, "CANTELOP_COMPUTE_INGRESS_URL"),
    token: required(environment, "CANTELOP_COMPUTE_CONTROL_TOKEN"),
  }, request, tenantForApp);
}

function validateCommand(command: ComputeAppRegistrationCommand): void {
  if (!resourcePatterns.user.test(command.ownerUserID) || !resourcePatterns.app.test(command.appID) ||
      !slugPattern.test(command.appSlug) ||
      !Number.isInteger(command.internalPort) || command.internalPort < 1 || command.internalPort > 65535) {
    throw new TypeError("invalid compute App registration command");
  }
}

function validateWorkspaceCommand(command: ComputeWorkspaceRegistrationCommand): void {
  validateArchiveCommand(command);
  if (command.registration && !resourcePatterns.user.test(command.registration.computeTenantID)) throw new TypeError("invalid Workspace registration context");
  if (!resourcePatterns.workspace.test(command.workspaceID) ||
      !slugPattern.test(command.workspaceSlug)) throw new TypeError("invalid compute Workspace registration command");
}

function validateWorkspaceArchiveCommand(command: ComputeWorkspaceArchiveCommand): void {
  validateArchiveCommand(command);
  if (!resourcePatterns.workspace.test(command.workspaceID)) throw new TypeError("invalid compute Workspace archive command");
}

function validateArchiveCommand(command: ComputeAppArchiveCommand): void {
  if (!resourcePatterns.user.test(command.ownerUserID) || !resourcePatterns.app.test(command.appID)) {
    throw new TypeError("invalid compute App archive command");
  }
}

function validateConfigurationCommand(command: ComputeConfigurationCommand): void {
  validateArchiveCommand(command);
  if (!Number.isSafeInteger(command.revision) || command.revision < 1 || command.revision > Number.MAX_SAFE_INTEGER ||
      !Array.isArray(command.values) || command.values.length > 128) {
    throw new TypeError("invalid compute configuration command");
  }
  const names = new Set<string>();
  let bytes = 0;
  for (const value of command.values) {
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(value.name) || value.name.startsWith("CANTELOP_") ||
        (value.kind !== "variable" && value.kind !== "secret") || Buffer.byteLength(value.value) > 64 * 1024 ||
        value.value.includes("\0") || names.has(value.name)) throw new TypeError("invalid compute configuration command");
    names.add(value.name);
    bytes += value.name.length + Buffer.byteLength(value.value);
  }
  if (bytes > 512 * 1024) throw new TypeError("invalid compute configuration command");
}

async function validateDocument(response: Response, expected: Record<string, unknown>): Promise<void> {
  let document: unknown;
  try {
    document = await response.json();
  } catch {
    throw new ComputeRegistrationProviderError(true);
  }
  if (!plainObject(document) || Object.keys(document).sort().join(",") !== Object.keys(expected).sort().join(",") ||
      Object.entries(expected).some(([key, value]) => document[key] !== value)) {
    throw new ComputeRegistrationProviderError(true);
  }
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
