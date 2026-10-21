import { APP_ID_PATTERN, EDGE_HEADERS, REQUEST_ID_PATTERN } from "@cantelop/edge-contracts";

export interface EdgeLogObservation {
  event_id: string;
  schema_version: 1;
  source: "edge";
  type: "log.recorded";
  occurred_at: string;
  app_id: string;
  request_id: string;
  trace_id: string;
  log_id: string;
  severity: "debug" | "info" | "warn" | "error";
  body: string;
  attributes: Record<string, unknown>;
}

interface TailLog {
  message: unknown;
  level: string;
  timestamp: number;
}

interface TailException {
  name: string;
  message: string;
  timestamp: number;
}

interface TailDiagnostic {
  channel: string;
  message: unknown;
  timestamp: number;
}

export interface EdgeTailItem {
  scriptName: string | null;
  eventTimestamp: number | null;
  event: TraceItem["event"];
  logs: TailLog[];
  exceptions: TailException[];
  diagnosticsChannelEvents: TailDiagnostic[];
  outcome: string;
  cpuTime: number;
  wallTime: number;
  truncated: boolean;
}

const MAX_BODY = 16_384;

export async function observationsFromTail(items: EdgeTailItem[]): Promise<EdgeLogObservation[]> {
  const observations: EdgeLogObservation[] = [];
  for (const item of items) {
    const scope = requestScope(item);
    if (!scope) {
      if (item.scriptName !== "cantelop-dispatcher") continue;
      for (const [index, log] of item.logs.entries()) {
        const dispatcherScope = scopeFromDispatcherLog(log.message);
        if (!dispatcherScope) continue;
        observations.push(await observation(dispatcherScope, log.timestamp, severity(log.level),
          formatConsoleMessage(log.message), {
            surface: "edge", producer: "dispatcher", script_name: item.scriptName, source: `console.${log.level}`,
          }, `dispatcher-log:${index}`));
      }
      continue;
    }
    const common = {
      surface: "edge",
      producer: "user_worker",
      script_name: item.scriptName,
    };
    for (const [index, log] of item.logs.entries()) {
      observations.push(await observation(scope, log.timestamp, severity(log.level),
        formatConsoleMessage(log.message), { ...common, source: `console.${log.level}` }, `log:${index}`));
    }
    for (const [index, exception] of item.exceptions.entries()) {
      observations.push(await observation(scope, exception.timestamp, "error",
        bounded(`${exception.name}: ${exception.message}`), { ...common, source: "exception", exception_name: exception.name },
        `exception:${index}`));
    }
    for (const [index, diagnostic] of item.diagnosticsChannelEvents.entries()) {
      observations.push(await observation(scope, diagnostic.timestamp, "debug",
        bounded(formatValue(diagnostic.message) || "(empty diagnostic message)"), { ...common, source: "diagnostics_channel", channel: diagnostic.channel },
        `diagnostic:${index}`));
    }
    const startedAt = validTimestamp(item.eventTimestamp) ? item.eventTimestamp : Date.now();
    const completedAt = startedAt + (finiteNonnegative(item.wallTime) ?? 0);
    observations.push(await observation(scope, completedAt, failedOutcome(item.outcome) ? "error" : "info",
      invocationBody(item), {
        ...common,
        source: "invocation",
        outcome: item.outcome,
        cpu_time_ms: finiteNonnegative(item.cpuTime),
        wall_time_ms: finiteNonnegative(item.wallTime),
        truncated: item.truncated,
      }, "invocation"));
    if (item.truncated) {
      observations.push(await observation(scope, completedAt, "warn", "Cloudflare truncated edge invocation logs", {
        ...common, source: "cloudflare", truncated: true,
      }, "truncated"));
    }
  }
  return observations;
}

function scopeFromDispatcherLog(values: unknown): { appID: string; requestID: string; traceID: string } | null {
  for (const value of Array.isArray(values) ? values : [values]) {
    if (typeof value !== "string") continue;
    try {
      const parsed = JSON.parse(value) as unknown;
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) continue;
      const record = parsed as Record<string, unknown>;
      if (typeof record.app_id !== "string" || !APP_ID_PATTERN.test(record.app_id) ||
          typeof record.request_id !== "string" || !REQUEST_ID_PATTERN.test(record.request_id)) continue;
      return { appID: record.app_id, requestID: record.request_id, traceID: record.request_id.slice("req_".length) };
    } catch {
      // Dispatcher diagnostics are structured JSON; unrelated console output
      // is intentionally ignored because it cannot be assigned to a tenant.
    }
  }
  return null;
}

function requestScope(item: EdgeTailItem): { appID: string; requestID: string; traceID: string } | null {
  if (!item.scriptName || !APP_ID_PATTERN.test(item.scriptName)) return null;
  const headers = item.event && "request" in item.event ? item.event.request?.headers : undefined;
  if (!headers) return null;
  const requestID = header(headers, EDGE_HEADERS.requestId);
  if (!requestID || !REQUEST_ID_PATTERN.test(requestID)) return null;
  return { appID: item.scriptName, requestID, traceID: requestID.slice("req_".length) };
}

async function observation(
  scope: { appID: string; requestID: string; traceID: string },
  timestamp: number,
  level: EdgeLogObservation["severity"],
  body: string,
  attributes: Record<string, unknown>,
  discriminator: string,
): Promise<EdgeLogObservation> {
  body = bounded(body);
  attributes = Object.fromEntries(Object.entries(attributes).map(([key, value]) => [
    key, typeof value === "string" ? boundedAttribute(value) : value,
  ]));
  const occurredAt = new Date(validTimestamp(timestamp) ? timestamp : Date.now()).toISOString();
  const identity = await digest(`${scope.appID}\0${scope.requestID}\0${occurredAt}\0${discriminator}\0${body}`);
  const logID = `log_${identity.slice(0, 32)}`;
  return {
    event_id: logID,
    schema_version: 1,
    source: "edge",
    type: "log.recorded",
    occurred_at: occurredAt,
    app_id: scope.appID,
    request_id: scope.requestID,
    trace_id: scope.traceID,
    log_id: logID,
    severity: level,
    body,
    attributes,
  };
}

async function digest(value: string): Promise<string> {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(bytes)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function header(headers: Record<string, string>, name: string): string | undefined {
  const target = name.toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === target) return value;
  }
  return undefined;
}

function severity(value: string): EdgeLogObservation["severity"] {
  if (value === "debug") return "debug";
  if (value === "warn") return "warn";
  if (value === "error") return "error";
  return "info";
}

function failedOutcome(value: string): boolean {
  return value !== "ok" && value !== "success" && value !== "canceled";
}

function invocationBody(item: EdgeTailItem): string {
  const cpu = finiteNonnegative(item.cpuTime);
  const wall = finiteNonnegative(item.wallTime);
  return `edge Worker invocation completed · outcome ${item.outcome}` +
    (cpu === undefined ? "" : ` · CPU ${cpu} ms`) +
    (wall === undefined ? "" : ` · wall ${wall} ms`);
}

function formatConsoleMessage(values: unknown): string {
  const parts = Array.isArray(values) ? values : [values];
  return bounded(parts.map(formatValue).join(" ") || "(empty console message)");
}

function formatValue(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    const encoded = JSON.stringify(value);
    return encoded === undefined ? String(value) : encoded;
  } catch {
    return String(value);
  }
}

function bounded(value: string): string {
  const normalized = value.replace(/[\0\r]/g, " ");
  return normalized.length <= MAX_BODY ? normalized : `${normalized.slice(0, MAX_BODY - 1)}…`;
}

function boundedAttribute(value: string): string {
  const normalized = value.replace(/[\0\r]/g, " ");
  return normalized.length <= 256 ? normalized : `${normalized.slice(0, 255)}…`;
}

function validTimestamp(value: number | null): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 && value <= 8.64e15;
}

function finiteNonnegative(value: number): number | undefined {
  return Number.isFinite(value) && value >= 0 ? value : undefined;
}
