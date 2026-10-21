import { assertResourceID } from "../domain/ids";
import { APIProviderError, type APIProvider, type APIProviderRelease } from "./provider";

interface CloudflareProviderConfig {
  accountID: string;
  dispatchNamespace: string;
  apiToken: string;
  compatibilityDate: string;
  apiBaseURL?: string;
  fetch?: typeof fetch;
}

export class CloudflareAPIProvider implements APIProvider {
  private readonly apiBaseURL: string;
  private readonly request: typeof fetch;

  constructor(private readonly config: CloudflareProviderConfig) {
    if (!/^[0-9a-f]{32}$/.test(config.accountID) ||
        !/^[a-z0-9][a-z0-9-]{0,62}$/.test(config.dispatchNamespace) ||
        !config.apiToken || config.apiToken.trim() !== config.apiToken || config.apiToken.includes("\0") ||
        !/^\d{4}-\d{2}-\d{2}$/.test(config.compatibilityDate) || Number.isNaN(Date.parse(`${config.compatibilityDate}T00:00:00Z`))) {
      throw new TypeError("invalid Cloudflare API provider configuration");
    }
    const base = new URL(config.apiBaseURL ?? "https://api.cloudflare.com/client/v4");
    if (base.protocol !== "https:" || base.username || base.password || base.search || base.hash) {
      throw new TypeError("invalid Cloudflare API base URL");
    }
    this.apiBaseURL = base.toString().replace(/\/$/, "");
    this.request = config.fetch ?? fetch;
  }

  async deploy(appID: string, releaseID: string, module: Buffer): Promise<APIProviderRelease> {
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    if (module.length < 1 || module.length > 1 << 20) throw new TypeError("invalid API module");
    new TextDecoder("utf-8", { fatal: true }).decode(module);
    const form = new FormData();
    form.set("metadata", new Blob([JSON.stringify({
      main_module: "worker.mjs",
      compatibility_date: this.config.compatibilityDate,
      observability: { enabled: true },
      bindings: [],
    })], { type: "application/json" }));
    form.set("worker.mjs", new Blob([new Uint8Array(module)], { type: "application/javascript+module" }), "worker.mjs");
    const response = await this.request(this.scriptEndpoint(releaseID), {
      method: "PUT",
      headers: this.headers(),
      body: form,
      signal: AbortSignal.timeout(30_000),
    });
    const payload = await boundedJSON(response);
    const envelope = payload as { success?: unknown; result?: { id?: unknown; tag?: unknown; etag?: unknown } };
    if (!response.ok || envelope.success !== true) throw new APIProviderError(`Cloudflare deployment failed with HTTP ${response.status}`, `api_provider_http_${response.status}`);
    const providerReleaseID = typeof envelope.result?.tag === "string" && envelope.result.tag
      ? envelope.result.tag
      : envelope.result?.id;
    if (typeof providerReleaseID !== "string" || !providerReleaseID || providerReleaseID.length > 512 || /[\0\r\n]/.test(providerReleaseID) ||
        (envelope.result?.etag !== undefined && (typeof envelope.result.etag !== "string" || envelope.result.etag.length > 512))) {
      throw new Error("Cloudflare deployment response omitted release identity");
    }
    return { id: providerReleaseID, etag: typeof envelope.result?.etag === "string" ? envelope.result.etag : "", scriptName: releaseID };
  }

  async removeRelease(appID: string, releaseID: string): Promise<void> {
    assertResourceID("app", appID);
    assertResourceID("release", releaseID);
    let response: Response;
    try {
      response = await this.request(this.scriptEndpoint(releaseID), {
        method: "DELETE", headers: this.headers(), signal: AbortSignal.timeout(30_000),
      });
    } catch { throw new APIProviderError("Cloudflare release removal failed", "api_provider_unavailable"); }
    if (response.status === 404) return;
    const payload = await boundedJSON(response);
    if (!response.ok || typeof payload !== "object" || payload === null || (payload as { success?: unknown }).success !== true) {
      throw new APIProviderError(`Cloudflare release removal failed with HTTP ${response.status}`, `api_provider_http_${response.status}`);
    }
  }

  private scriptEndpoint(scriptName: string): string {
    return `${this.apiBaseURL}/accounts/${this.config.accountID}/workers/dispatch/namespaces/${this.config.dispatchNamespace}/scripts/${scriptName}`;
  }

  private headers(): HeadersInit {
    return { Authorization: `Bearer ${this.config.apiToken}`, "User-Agent": "cantelop-api-deployer/1" };
  }
}

export function configuredCloudflareAPIProvider(environment: NodeJS.ProcessEnv = process.env): CloudflareAPIProvider {
  return new CloudflareAPIProvider({
    accountID: required(environment, "CLOUDFLARE_ACCOUNT_ID"),
    dispatchNamespace: required(environment, "CLOUDFLARE_DISPATCH_NAMESPACE"),
    apiToken: required(environment, "CLOUDFLARE_API_TOKEN"),
    compatibilityDate: required(environment, "CLOUDFLARE_COMPATIBILITY_DATE"),
    apiBaseURL: environment.CLOUDFLARE_API_BASE_URL?.trim(),
  });
}

async function boundedJSON(response: Response): Promise<unknown> {
  if (!response.body) throw new Error("Cloudflare returned an empty response");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 1 << 20) {
        await reader.cancel("response exceeds limit");
        throw new Error("Cloudflare response exceeds limit");
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const body = Buffer.concat(chunks.map((chunk) => Buffer.from(chunk)), size);
  try { return JSON.parse(body.toString("utf8")) as unknown; }
  catch { throw new Error(`Cloudflare returned invalid JSON with HTTP ${response.status}`); }
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
