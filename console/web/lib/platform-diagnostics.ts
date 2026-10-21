import "server-only";

const WINDOW_MS = 6 * 60 * 60 * 1_000;
const MAX_EVENTS = 100;

export interface PlatformDiagnosticEvent {
  timestamp: string;
  outcome: "failed" | "rejected";
  stage: "route" | "dispatch";
  status: number;
  reason: string;
  request_id: string;
  hostname: string;
  method: string;
  path: string;
  app_id?: string;
}

interface PlatformDiagnosticsConfig {
  accountID: string;
  apiToken: string;
  service?: string;
  apiBaseURL?: string;
  fetch?: typeof fetch;
  now?: () => number;
}

export class CloudflarePlatformDiagnostics {
  private readonly baseURL: string;
  private readonly request: typeof fetch;
  private readonly now: () => number;
  private readonly service: string;

  constructor(private readonly config: PlatformDiagnosticsConfig) {
    if (!/^[0-9a-f]{32}$/.test(config.accountID) ||
        !config.apiToken || config.apiToken.trim() !== config.apiToken || config.apiToken.includes("\0")) {
      throw new TypeError("invalid Cloudflare diagnostics configuration");
    }
    this.service = config.service ?? "cantelop-dispatcher";
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(this.service)) {
      throw new TypeError("invalid Cloudflare dispatcher service");
    }
    const base = new URL(config.apiBaseURL ?? "https://api.cloudflare.com/client/v4");
    if (base.protocol !== "https:" || base.username || base.password || base.search || base.hash) {
      throw new TypeError("invalid Cloudflare diagnostics API URL");
    }
    this.baseURL = base.toString().replace(/\/$/, "");
    this.request = config.fetch ?? fetch;
    this.now = config.now ?? Date.now;
  }

  async list(): Promise<{ items: PlatformDiagnosticEvent[]; window_hours: 6 }> {
    const to = this.now();
    const response = await this.request(
      `${this.baseURL}/accounts/${this.config.accountID}/workers/observability/telemetry/query`,
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${this.config.apiToken}`,
          "Content-Type": "application/json",
          "User-Agent": "cantelop-console-platform-diagnostics/1",
        },
        body: JSON.stringify({
          queryId: "cantelop-platform-diagnostics",
          dry: true,
          view: "events",
          limit: MAX_EVENTS,
          timeframe: { from: to - WINDOW_MS, to },
          parameters: {
            datasets: ["cloudflare-workers"],
            filterCombination: "and",
            filters: [
              { key: "$metadata.service", operation: "eq", type: "string", value: this.service },
              { key: "$metadata.message", operation: "includes", type: "string", value: '"source":"cantelop_dispatcher"' },
            ],
          },
        }),
        signal: AbortSignal.timeout(10_000),
      },
    );
    const document = await boundedJSON(response);
    if (!response.ok || !plainObject(document) || document.success !== true ||
        !plainObject(document.result) || !plainObject(document.result.events) ||
        !Array.isArray(document.result.events.events)) {
      throw new Error(`Cloudflare diagnostics query failed with HTTP ${response.status}`);
    }
    const items = document.result.events.events
      .map(parsePlatformDiagnostic)
      .filter((event): event is PlatformDiagnosticEvent => event !== null)
      .sort((left, right) => right.timestamp.localeCompare(left.timestamp));
    return { items, window_hours: 6 };
  }
}

export function configuredPlatformDiagnostics(environment: NodeJS.ProcessEnv = process.env): CloudflarePlatformDiagnostics {
  return new CloudflarePlatformDiagnostics({
    accountID: required(environment, "CLOUDFLARE_ACCOUNT_ID"),
    apiToken: required(environment, "CLOUDFLARE_OBSERVABILITY_TOKEN"),
    service: environment.CLOUDFLARE_DISPATCHER_SERVICE?.trim(),
    apiBaseURL: environment.CLOUDFLARE_API_BASE_URL?.trim(),
  });
}

function parsePlatformDiagnostic(value: unknown): PlatformDiagnosticEvent | null {
  if (!plainObject(value) || typeof value.timestamp !== "number" || !Number.isFinite(value.timestamp)) return null;
  const metadata = plainObject(value.$metadata) ? value.$metadata : undefined;
  const rawSource = value.source;
  const payload = [rawSource, plainObject(rawSource) ? rawSource.message : undefined, metadata?.message]
    .map(parsePayload)
    .find((candidate) => plainObject(candidate) && candidate.source === "cantelop_dispatcher");
  if (!plainObject(payload) ||
      !["failed", "rejected"].includes(String(payload.outcome)) ||
      !["route", "dispatch"].includes(String(payload.stage)) ||
      !Number.isInteger(payload.status) || Number(payload.status) < 400 || Number(payload.status) > 599) return null;
  for (const field of ["reason", "request_id", "hostname", "method", "path"] as const) {
    if (!safeString(payload[field], field === "path" ? 512 : 128)) return null;
  }
  const event: PlatformDiagnosticEvent = {
    timestamp: new Date(value.timestamp).toISOString(),
    outcome: payload.outcome as PlatformDiagnosticEvent["outcome"],
    stage: payload.stage as PlatformDiagnosticEvent["stage"],
    status: Number(payload.status),
    reason: payload.reason as string,
    request_id: payload.request_id as string,
    hostname: payload.hostname as string,
    method: payload.method as string,
    path: payload.path as string,
  };
  if (safeString(payload.app_id, 128)) event.app_id = payload.app_id;
  return event;
}

function safeString(value: unknown, limit: number): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= limit && !/[\0\r\n]/.test(value);
}

function parsePayload(value: unknown): unknown {
  if (typeof value !== "string") return value;
  try { return JSON.parse(value) as unknown; }
  catch { return undefined; }
}

async function boundedJSON(response: Response): Promise<unknown> {
  const text = await response.text();
  if (Buffer.byteLength(text) > 1 << 20) throw new Error("Cloudflare diagnostics response is too large");
  try { return JSON.parse(text) as unknown; }
  catch { throw new Error(`Cloudflare diagnostics returned invalid JSON with HTTP ${response.status}`); }
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = environment[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
