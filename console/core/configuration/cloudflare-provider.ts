import { assertResourceID } from "../domain/ids";
import { ConfigurationProviderError, type APIConfigurationProvider, type ConfigurationProjection } from "./provider";

interface CloudflareConfig {
  accountID: string;
  dispatchNamespace: string;
  apiToken: string;
  apiBaseURL?: string;
}

export class CloudflareConfigurationProvider implements APIConfigurationProvider {
  private readonly baseURL: string;

  constructor(
    private readonly config: CloudflareConfig,
    private readonly request: typeof fetch = fetch,
  ) {
    if (!/^[0-9a-f]{32}$/.test(config.accountID) || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(config.dispatchNamespace) ||
        !config.apiToken || config.apiToken.trim() !== config.apiToken || config.apiToken.includes("\0")) {
      throw new TypeError("invalid Cloudflare configuration provider");
    }
    const base = new URL(config.apiBaseURL ?? "https://api.cloudflare.com/client/v4");
    if (base.protocol !== "https:" || base.username || base.password || base.search || base.hash) {
      throw new TypeError("invalid Cloudflare API base URL");
    }
    this.baseURL = base.toString().replace(/\/$/, "");
  }

  async replaceConfiguration(projection: ConfigurationProjection): Promise<void> {
    validateProjection(projection);
    const bindings = [...projection.values,...(projection.systemBindings ?? [])]
      .sort((left, right) => left.name.localeCompare(right.name))
      .map((value) => ({ name: value.name, text: value.value, type: value.kind === "secret" ? "secret_text" : "plain_text" }));
    const form = new FormData();
    form.set("settings", new Blob([JSON.stringify({ bindings })], { type: "application/json" }));
    let response: Response;
    try {
      response = await this.request(
        `${this.baseURL}/accounts/${this.config.accountID}/workers/dispatch/namespaces/${this.config.dispatchNamespace}/scripts/${projection.scriptName ?? projection.appID}/settings`,
        {
          method: "PATCH",
          headers: { Authorization: `Bearer ${this.config.apiToken}`, "User-Agent": "cantelop-config-projector/1" },
          body: form,
          signal: AbortSignal.timeout(30_000),
        },
      );
    } catch {
      throw new ConfigurationProviderError(true);
    }
    let document: unknown;
    try { document = await boundedJSON(response); }
    catch { throw new ConfigurationProviderError(true); }
    if (!response.ok || !plainObject(document) || document.success !== true) {
      throw new ConfigurationProviderError(response.status === 404 || response.status === 409 || response.status === 429 || response.status >= 500);
    }
  }
}

export function configuredCloudflareConfigurationProvider(environment: NodeJS.ProcessEnv = process.env): CloudflareConfigurationProvider {
  return new CloudflareConfigurationProvider({
    accountID: required(environment, "CLOUDFLARE_ACCOUNT_ID"),
    dispatchNamespace: required(environment, "CLOUDFLARE_DISPATCH_NAMESPACE"),
    apiToken: required(environment, "CLOUDFLARE_API_TOKEN"),
    apiBaseURL: environment.CLOUDFLARE_API_BASE_URL?.trim(),
  });
}

function validateProjection(projection: ConfigurationProjection): void {
  assertResourceID("app", projection.appID);
  if (projection.scriptName !== undefined && projection.scriptName !== projection.appID) {
    assertResourceID("release", projection.scriptName);
  }
  if (!Number.isSafeInteger(projection.revision) || projection.revision < 0 || projection.values.length > 128) {
    throw new TypeError("invalid configuration projection");
  }
  if(projection.systemBindings!==undefined) {
    const values=projection.systemBindings;
    if((values.length!==2&&values.length!==4)||values[0].name!=="CANTELOP_INTEGRATION_TOKEN"||values[0].kind!=="secret"||!/^[0-9a-f]{64}$/.test(values[0].value)||values[1].name!=="CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS"||values[1].kind!=="variable"||values[1].value!=="900") throw new TypeError("invalid system bindings");
    // A rotation's grace period adds the previous token and its expiry.
    if(values.length===4&&(values[2].name!=="CANTELOP_INTEGRATION_TOKEN_PREVIOUS"||values[2].kind!=="secret"||!/^[0-9a-f]{64}$/.test(values[2].value)||values[2].value===values[0].value||values[3].name!=="CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT"||values[3].kind!=="variable"||!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(values[3].value)||Number.isNaN(Date.parse(values[3].value)))) throw new TypeError("invalid system bindings");
  }
  const names = new Set<string>();
  let bytes = 0;
  for (const value of projection.values) {
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(value.name) || value.name.startsWith("CANTELOP_") ||
        (value.kind !== "variable" && value.kind !== "secret") || Buffer.byteLength(value.value) > 64 * 1024 ||
        value.value.includes("\0") || names.has(value.name)) throw new TypeError("invalid configuration projection");
    names.add(value.name);
    bytes += value.name.length + Buffer.byteLength(value.value);
  }
  if (bytes > 512 * 1024) throw new TypeError("invalid configuration projection");
}

async function boundedJSON(response: Response): Promise<unknown> {
  const text = await response.text();
  if (Buffer.byteLength(text) > 1 << 20) throw new Error("response too large");
  return JSON.parse(text) as unknown;
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
