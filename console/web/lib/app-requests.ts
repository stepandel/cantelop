import "server-only";

// The wire shape lives with the other telemetry records so client components
// can name it without importing this server-only module.
import type { AppRequestInvocation, AppRequestLog } from "@/lib/telemetry-types";
import { readBoundedUTF8Body } from "@core/http/bounded-body";

export type { AppRequestInvocation, AppRequestLog };

const WINDOW_MS = 6 * 60 * 60 * 1_000;
const MAX_INVOCATIONS = 100;
const MAX_LOG_MESSAGE = 4_096;

interface AppRequestsConfig {
  accountID: string;
  apiToken: string;
  apiBaseURL?: string;
  fetch?: typeof fetch;
  now?: () => number;
}

export class CloudflareAppRequests {
  private readonly baseURL: string;
  private readonly request: typeof fetch;
  private readonly now: () => number;

  constructor(private readonly config: AppRequestsConfig) {
    if (!/^[0-9a-f]{32}$/.test(config.accountID) ||
        !config.apiToken || config.apiToken.trim() !== config.apiToken || config.apiToken.includes("\0")) {
      throw new TypeError("invalid Cloudflare requests configuration");
    }
    const base = new URL(config.apiBaseURL ?? "https://api.cloudflare.com/client/v4");
    if (base.protocol !== "https:" || base.username || base.password || base.search || base.hash) {
      throw new TypeError("invalid Cloudflare requests API URL");
    }
    this.baseURL = base.toString().replace(/\/$/, "");
    this.request = config.fetch ?? fetch;
    this.now = config.now ?? Date.now;
  }

  async list(appID: string): Promise<{ items: AppRequestInvocation[]; window_hours: 6 }> {
    if (!/^app_[0-9a-f]{32}$/.test(appID)) throw new TypeError("invalid App ID");
    const to = this.now();
    const response = await this.request(
      `${this.baseURL}/accounts/${this.config.accountID}/workers/observability/telemetry/query`,
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${this.config.apiToken}`,
          "Content-Type": "application/json",
          "User-Agent": "cantelop-console-app-requests/1",
        },
        body: JSON.stringify({
          queryId: "cantelop-app-requests",
          dry: true,
          view: "invocations",
          limit: MAX_INVOCATIONS,
          timeframe: { from: to - WINDOW_MS, to },
          parameters: {
            datasets: ["cloudflare-workers"],
            filterCombination: "and",
            filters: [{ key: "$metadata.service", operation: "eq", type: "string", value: appID }],
          },
        }),
        signal: AbortSignal.timeout(10_000),
      },
    );
    const document = await boundedJSON(response);
    if (!response.ok || !plainObject(document) || document.success !== true ||
        !plainObject(document.result) || !plainObject(document.result.invocations)) {
      throw new Error(`Cloudflare request query failed with HTTP ${response.status}`);
    }
    const items = Object.entries(document.result.invocations)
      .map(([requestID, events]) => parseInvocation(requestID, events))
      .filter((event): event is AppRequestInvocation => event !== null)
      .sort((left, right) => right.timestamp.localeCompare(left.timestamp));
    return { items, window_hours: 6 };
  }
}

export function configuredAppRequests(environment: NodeJS.ProcessEnv = process.env): CloudflareAppRequests {
  return new CloudflareAppRequests({
    accountID: required(environment, "CLOUDFLARE_ACCOUNT_ID"),
    apiToken: required(environment, "CLOUDFLARE_OBSERVABILITY_TOKEN"),
    apiBaseURL: environment.CLOUDFLARE_API_BASE_URL?.trim(),
  });
}

function parseInvocation(requestID: string, value: unknown): AppRequestInvocation | null {
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/.test(requestID) || !Array.isArray(value) || value.length === 0) return null;
  const events = value.filter(plainObject);
  if (events.length === 0) return null;
  const invocationEvent = events.find((event) => {
    const metadata = plainObject(event.$metadata) ? event.$metadata : undefined;
    return metadata?.type === "cf-worker-event" || typeof metadata?.statusCode === "number";
  }) ?? events[0];
  const metadata = plainObject(invocationEvent.$metadata) ? invocationEvent.$metadata : {};
  const workers = plainObject(invocationEvent.$workers) ? invocationEvent.$workers : {};
  const timestamps = events
    .map((event) => event.timestamp)
    .filter((timestamp): timestamp is number => typeof timestamp === "number" && Number.isFinite(timestamp));
  if (timestamps.length === 0) return null;
  const invocation: AppRequestInvocation = {
    request_id: requestID,
    timestamp: new Date(Math.min(...timestamps)).toISOString(),
    logs: events.map(parseLog).filter((log): log is AppRequestLog => log !== null),
  };
  if (Number.isInteger(metadata.statusCode) && Number(metadata.statusCode) >= 100 && Number(metadata.statusCode) <= 599) {
    invocation.status = Number(metadata.statusCode);
  }
  if (typeof metadata.duration === "number" && Number.isFinite(metadata.duration) && metadata.duration >= 0) {
    invocation.duration_ms = metadata.duration;
  }
  if (safeString(metadata.outcome, 64)) invocation.outcome = metadata.outcome;
  else if (safeString(workers.outcome, 64)) invocation.outcome = workers.outcome;
  const target = requestTarget(metadata);
  if (target.method) invocation.method = target.method;
  if (target.path) invocation.path = target.path;
  return invocation;
}

function parseLog(event: Record<string, unknown>): AppRequestLog | null {
  if (typeof event.timestamp !== "number" || !Number.isFinite(event.timestamp)) return null;
  const metadata = plainObject(event.$metadata) ? event.$metadata : {};
  if (metadata.type === "cf-worker-event") return null;
  let message: string | undefined;
  if (safeString(metadata.message, MAX_LOG_MESSAGE)) message = metadata.message;
  else if (typeof event.source === "string" && safeString(event.source, MAX_LOG_MESSAGE)) message = event.source;
  else if (plainObject(event.source) && safeString(event.source.message, MAX_LOG_MESSAGE)) message = event.source.message;
  if (!message) return null;
  return {
    timestamp: new Date(event.timestamp).toISOString(),
    level: safeString(metadata.level, 32) ? metadata.level : "log",
    message,
  };
}

function requestTarget(metadata: Record<string, unknown>): { method?: string; path?: string } {
  const target: { method?: string; path?: string } = {};
  if (safeString(metadata.trigger, 256)) {
    const match = /^([A-Z]+)\s+(.+)$/.exec(metadata.trigger);
    if (match) {
      target.method = match[1];
      target.path = pathOnly(match[2]);
    }
  }
  if (!target.path && safeString(metadata.url, 2_048)) target.path = pathOnly(metadata.url);
  return target;
}

function pathOnly(value: string): string | undefined {
  try { return new URL(value, "https://request.invalid").pathname; }
  catch { return undefined; }
}

function safeString(value: unknown, limit: number): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= limit && !/[\0\r]/.test(value);
}

async function boundedJSON(response: Response): Promise<unknown> {
  let text: string;
  try { text = response.body ? await readBoundedUTF8Body(response.body, 2 << 20) : ""; }
  catch (error) {
    const reason = error instanceof Error && error.message.includes("exceeds limit") ? "is too large" : "is unreadable";
    throw new Error(`Cloudflare request response ${reason} with HTTP ${response.status}`);
  }
  try { return JSON.parse(text) as unknown; }
  catch { throw new Error(`Cloudflare requests returned invalid JSON with HTTP ${response.status}`); }
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
