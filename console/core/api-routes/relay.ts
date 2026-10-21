import type { APIRoute } from "../api-deployments/artifact";
import { ControlError } from "../domain/errors";

/**
 * Sends one request to an App's public API on the operator's behalf, from
 * the console's own network position, and returns what came back.
 *
 * The console never learns a URL from the client. The target is the App's
 * public origin, taken from its slug, and a path the live release declares.
 * That keeps this a way to exercise the App's own API and nothing else: no
 * other host, no undeclared path, no redirect off the origin. The App sees an
 * ordinary public request, with whatever authorization the operator typed;
 * the platform adds no privilege of its own.
 */

export const relayLimits = Object.freeze({
  bodyBytes: 256 * 1024,
  responseBytes: 1024 * 1024,
  timeoutMs: 30_000,
  headerCount: 16,
  headerNameLength: 64,
  headerValueLength: 4_096,
});

/** Header the relay sets on every request it sends, so the App and the traces can tell them apart. */
export const RELAY_MARKER_HEADER = "X-Cantelop-Console";

// Headers the operator may set. Everything else is either set by the relay
// (host, length, marker), hop-by-hop, or would let the request impersonate
// the edge; the App's own auth fits in Authorization or a custom X- header.
const HEADER_NAME = /^[A-Za-z0-9-]+$/;
const FORBIDDEN_HEADERS = new Set([
  "host", "content-length", "transfer-encoding", "connection", "upgrade", "expect",
  "te", "trailer", "keep-alive", "proxy-authorization", "proxy-connection",
  "cookie", "origin", "referer", "forwarded",
]);
const FORBIDDEN_HEADER_PREFIXES = ["x-cantelop-", "x-forwarded-", "cf-", "sec-", "proxy-"];
const HEADER_VALUE = /^[\x20-\x7e]*$/;

export interface RelayRequest {
  method: APIRoute["method"];
  path: string;
  // Sent as given, after the path; never parsed by the relay.
  query?: string;
  headers?: Record<string, string>;
  // UTF-8 text. Absent for GET and HEAD.
  body?: string;
}

export interface RelayResponse {
  request_id?: string;
  status: number;
  headers: Record<string, string>;
  body: string;
  body_truncated: boolean;
  duration_ms: number;
  // The exact request the relay sent, for the operator's record.
  sent: { method: string; url: string; headers: Record<string, string> };
}

const METHODS_WITH_BODY = new Set(["PATCH", "POST", "PUT", "DELETE"]);

/** Validates an operator's request against the routes the App declares. */
export function relayRequest(input: unknown, declared: readonly APIRoute[]): RelayRequest {
  if (!plainObject(input) || !onlyKeys(input, ["method", "path", "query", "headers", "body"])) {
    throw new TypeError("invalid relay request");
  }
  const { method, path, query, headers, body } = input;
  if (typeof method !== "string" || typeof path !== "string" ||
      !declared.some((route) => route.method === method && route.path === path)) {
    throw new ControlError("not_found");
  }
  if (query !== undefined && (typeof query !== "string" || query.length > 2_048 || !/^[\x21-\x7e]*$/.test(query) || query.includes("#"))) {
    throw new TypeError("invalid relay request");
  }
  const sentHeaders: Record<string, string> = {};
  if (headers !== undefined) {
    if (!plainObject(headers) || Object.keys(headers).length > relayLimits.headerCount) {
      throw new TypeError("invalid relay request");
    }
    for (const [name, value] of Object.entries(headers)) {
      const lower = name.toLowerCase();
      if (!HEADER_NAME.test(name) || name.length > relayLimits.headerNameLength || FORBIDDEN_HEADERS.has(lower) ||
          FORBIDDEN_HEADER_PREFIXES.some((prefix) => lower.startsWith(prefix)) ||
          typeof value !== "string" || value.length > relayLimits.headerValueLength || !HEADER_VALUE.test(value)) {
        throw new TypeError("invalid relay request");
      }
      sentHeaders[lower] = value;
    }
  }
  if (body !== undefined) {
    if (typeof body !== "string" || !METHODS_WITH_BODY.has(method) || Buffer.byteLength(body) > relayLimits.bodyBytes) {
      throw new TypeError("invalid relay request");
    }
  }
  return {
    method: method as APIRoute["method"],
    path,
    ...(query === undefined || query === "" ? {} : { query }),
    ...(Object.keys(sentHeaders).length === 0 ? {} : { headers: sentHeaders }),
    ...(body === undefined ? {} : { body }),
  };
}

export type RelayFetch = (url: string, init: RequestInit) => Promise<Response>;

export interface RelayOptions {
  fetch?: RelayFetch;
  now?: () => number;
  timeoutMs?: number;
}

export async function sendRelayRequest(
  origin: string,
  request: RelayRequest,
  options: RelayOptions = {},
): Promise<RelayResponse> {
  const fetchImplementation = options.fetch ?? fetch;
  const now = options.now ?? Date.now;
  const timeoutMs = options.timeoutMs ?? relayLimits.timeoutMs;
  if (!/^https:\/\/[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.cantelop\.dev$/.test(origin)) {
    throw new TypeError("invalid relay origin");
  }
  const url = `${origin}${request.path}${request.query === undefined ? "" : `?${request.query}`}`;
  const headers = new Headers(request.headers);
  headers.set(RELAY_MARKER_HEADER, "1");
  if (request.body !== undefined && !headers.has("content-type")) {
    headers.set("content-type", "application/json");
  }
  const sent = { method: request.method, url, headers: Object.fromEntries(headers.entries()) };
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const startedAt = now();
  let response: Response;
  try {
    response = await fetchImplementation(url, {
      method: request.method,
      headers,
      body: request.body,
      // A redirect could point anywhere; the App's answer is the answer.
      redirect: "manual",
      signal: controller.signal,
    });
  } catch (error) {
    clearTimeout(timer);
    throw new RelayError(controller.signal.aborted ? "timeout" : "unreachable", error);
  }
  let body = "";
  let truncated = false;
  try {
    const reader = response.body?.getReader();
    const chunks: Uint8Array[] = [];
    let received = 0;
    while (reader) {
      const { done, value } = await reader.read();
      if (done) break;
      if (received + value.byteLength > relayLimits.responseBytes) {
        chunks.push(value.subarray(0, relayLimits.responseBytes - received));
        truncated = true;
        await reader.cancel();
        break;
      }
      chunks.push(value);
      received += value.byteLength;
    }
    body = Buffer.concat(chunks).toString("utf8");
  } catch (error) {
    throw new RelayError(controller.signal.aborted ? "timeout" : "unreachable", error);
  } finally {
    clearTimeout(timer);
  }
  const requestID = response.headers.get("x-cantelop-request-id");
  return {
    ...(requestID !== null && /^req_[0-9a-f]{32}$/.test(requestID) ? { request_id: requestID } : {}),
    status: response.status,
    headers: Object.fromEntries([...response.headers.entries()].filter(([name]) => !name.startsWith("cf-") && name !== "set-cookie")),
    body,
    body_truncated: truncated,
    duration_ms: Math.max(0, now() - startedAt),
    sent,
  };
}

export class RelayError extends Error {
  constructor(readonly reason: "timeout" | "unreachable", cause?: unknown) {
    super(reason, { cause });
    this.name = "RelayError";
  }
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}

function onlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key));
}
