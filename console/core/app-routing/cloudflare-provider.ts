import { assertResourceID } from "../domain/ids";
import { appSlugPattern } from "../domain/model";
import { AppRoutingProviderError, type AppRouteCommand, type AppRoutingProvider } from "./provider";

interface CloudflareAppRoutingConfig {
  accountID: string;
  dispatchNamespace: string;
  routeNamespaceID: string;
  apiToken: string;
  apiBaseURL?: string;
}

export class CloudflareAppRoutingProvider implements AppRoutingProvider {
  private readonly baseURL: string;

  constructor(
    private readonly config: CloudflareAppRoutingConfig,
    private readonly request: typeof fetch = fetch,
  ) {
    if (!/^[0-9a-f]{32}$/.test(config.accountID) || !/^[0-9a-f]{32}$/.test(config.routeNamespaceID) ||
        !/^[a-z0-9][a-z0-9-]{0,62}$/.test(config.dispatchNamespace) || !config.apiToken ||
        config.apiToken.trim() !== config.apiToken || config.apiToken.includes("\0")) {
      throw new TypeError("invalid Cloudflare App routing provider");
    }
    const base = new URL(config.apiBaseURL ?? "https://api.cloudflare.com/client/v4");
    if (base.protocol !== "https:" || base.username || base.password || base.search || base.hash) {
      throw new TypeError("invalid Cloudflare API base URL");
    }
    this.baseURL = base.toString().replace(/\/$/, "");
  }

  async publish(command: AppRouteCommand): Promise<void> {
    validateCommand(command);
    const response = await this.send(this.routeEndpoint(command.appSlug), {
      method: "PUT",
      headers: { ...this.headers(), "content-type": "text/plain;charset=UTF-8" },
      body: command.appID,
    });
    await requireSuccessEnvelope(response);
  }

  async remove(command: AppRouteCommand): Promise<void> {
    validateCommand(command);
    // The slug can be reused as soon as the old App is archived. KV has no
    // compare-and-delete, so old cleanup must never delete its slug pointer:
    // a new App may publish that same key while this request is in flight.
    // The archive worker closes admission before this call and also deletes
    // release-scoped scripts. The stale pointer remains safe behind that gate.
    const script = await this.send(this.scriptEndpoint(command.appID), {
      method: "DELETE",
      headers: this.headers(),
    });
    if (script.status !== 404) await requireSuccessEnvelope(script);
  }

  private routeEndpoint(appSlug: string): string {
    return `${this.baseURL}/accounts/${this.config.accountID}/storage/kv/namespaces/${this.config.routeNamespaceID}/values/${appSlug}`;
  }

  private scriptEndpoint(appID: string): string {
    return `${this.baseURL}/accounts/${this.config.accountID}/workers/dispatch/namespaces/${this.config.dispatchNamespace}/scripts/${appID}`;
  }

  private headers(): Record<string, string> {
    return { Authorization: `Bearer ${this.config.apiToken}`, "User-Agent": "cantelop-app-router/1" };
  }

  private async send(input: string, init: RequestInit): Promise<Response> {
    try {
      return await this.request(input, { ...init, redirect: "error", signal: AbortSignal.timeout(30_000) });
    } catch {
      throw new AppRoutingProviderError(true);
    }
  }
}

export function configuredCloudflareAppRoutingProvider(
  environment: NodeJS.ProcessEnv = process.env,
  request: typeof fetch = fetch,
): CloudflareAppRoutingProvider {
  return new CloudflareAppRoutingProvider({
    accountID: required(environment, "CLOUDFLARE_ACCOUNT_ID"),
    dispatchNamespace: required(environment, "CLOUDFLARE_DISPATCH_NAMESPACE"),
    routeNamespaceID: required(environment, "CLOUDFLARE_ROUTE_NAMESPACE_ID"),
    apiToken: required(environment, "CLOUDFLARE_API_TOKEN"),
    apiBaseURL: environment.CLOUDFLARE_API_BASE_URL?.trim(),
  }, request);
}

function validateCommand(command: AppRouteCommand): void {
  assertResourceID("app", command.appID);
  if (!appSlugPattern.test(command.appSlug)) throw new TypeError("invalid App route command");
}

async function requireSuccessEnvelope(response: Response): Promise<void> {
  let document: unknown;
  try { document = JSON.parse(await boundedText(response, 1 << 20)) as unknown; }
  catch { throw new AppRoutingProviderError(true); }
  if (!response.ok || !plainObject(document) || document.success !== true) throw providerError(response.status);
}

async function boundedText(response: Response, limit: number): Promise<string> {
  const text = await response.text();
  if (Buffer.byteLength(text) > limit) throw new AppRoutingProviderError(true);
  return text;
}

function providerError(status: number): AppRoutingProviderError {
  return new AppRoutingProviderError(status === 404 || status === 409 || status === 429 || status >= 500);
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
