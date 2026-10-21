import {
  EDGE_HEADERS,
  RESERVED_HEADERS,
  RESPONSE_REQUEST_ID_HEADER,
  appSlugFromHostname,
  assertAppId,
  assertAPIInvocation,
  type APIInvocation,
} from "@cantelop/edge-contracts";
import { GATE_APP_HEADER, GATE_INVOCATION_HEADER } from "./release-gate";

export interface UserWorkerNamespace {
  get(
    name: string,
    args?: Record<string, unknown>,
    options?: { outbound?: Record<string, unknown> },
  ): { fetch(request: Request): Promise<Response> };
}

export interface AppRouteRegistry {
  get(key: string, type: "text"): Promise<string | null>;
}

export interface DispatcherEnv {
  CANTELOP_APP_DOMAIN?: string;
  USER_WORKERS: UserWorkerNamespace;
  APP_ROUTES: AppRouteRegistry;
  RELEASE_GATES: Pick<DurableObjectNamespace, "idFromName" | "get">;
}

export interface CompletedRequestObservation {
  event_id: string;
  schema_version: 1;
  source: "edge";
  type: "request.completed";
  occurred_at: string;
  app_id: string;
  request_id: string;
  trace_id: string;
  received_at: string;
  completed_at: string;
  method: string;
  path: string;
  status_code: number;
  duration_us: number;
  outcome: "completed" | "failed";
}

type RequestIDFactory = () => string;
export interface DispatcherDiagnostic {
  source: "cantelop_dispatcher";
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
type DiagnosticSink = (diagnostic: DispatcherDiagnostic) => void;
type CompletedRequestSink = (observation: CompletedRequestObservation) => void;
type Clock = () => number;

const RESERVED_APP_SLUGS = new Set(["console", "edge-origin", "release-gate", "www"]);

export async function dispatchPublicRequest(
  request: Request,
  env: DispatcherEnv,
  requestIDFactory: RequestIDFactory = generateRequestID,
  diagnosticSink: DiagnosticSink = emitDiagnostic,
  completedRequestSink: CompletedRequestSink = () => {},
  clock: Clock = Date.now,
): Promise<Response> {
  const url = new URL(request.url);
  const receivedAt = clock();
  const requestID = requestIDFactory();
  const reject = (
    status: number,
    reason: string,
    stage: DispatcherDiagnostic["stage"],
    appID?: string,
  ) => {
    diagnosticSink({
      source: "cantelop_dispatcher",
      outcome: status >= 500 ? "failed" : "rejected",
      stage,
      status,
      reason,
      request_id: requestID,
      hostname: url.hostname,
      method: request.method,
      path: url.pathname,
      ...(appID ? { app_id: appID } : {}),
    });
    return errorResponse(status, reason);
  };
  let appSlug: string;
  try {
    appSlug = appSlugFromHostname(url.hostname, env.CANTELOP_APP_DOMAIN);
  } catch {
    return reject(404, "app_not_found", "route");
  }
  const appId = await resolveConfiguredApp(appSlug, env);
  if (appId === null) {
    return reject(404, "app_not_found", "route");
  }

  const invocation = {
    request_id: requestID,
    app_id: appId,
    hostname: url.hostname,
  };
  try {
    assertAPIInvocation(invocation, env.CANTELOP_APP_DOMAIN);
  } catch {
    return reject(500, "dispatcher_unavailable", "dispatch", appId);
  }

  const headers = new Headers(request.headers);
  for (const header of RESERVED_HEADERS) {
    headers.delete(header);
  }
  headers.delete(GATE_APP_HEADER);
  headers.delete(GATE_INVOCATION_HEADER);
  // Mint this only after attacker-controlled reserved headers are removed.
  // Cloudflare includes it in the user Worker's Tail event, allowing edge
  // console output and exceptions to be attached to this Request.
  headers.set(EDGE_HEADERS.requestId, requestID);
  const body = request.method === "GET" || request.method === "HEAD" ? null : request.body;
  const sanitizedRequest = new Request(request.url, {
    method: request.method,
    headers,
    body,
    redirect: request.redirect,
  });

  let response: Response;
  try {
    const gateHeaders = new Headers(sanitizedRequest.headers);
    gateHeaders.set(GATE_APP_HEADER, appId);
    gateHeaders.set(GATE_INVOCATION_HEADER, JSON.stringify(invocation));
    const gatedRequest = new Request(sanitizedRequest.url, {
      method: sanitizedRequest.method,
      headers: gateHeaders,
      body: sanitizedRequest.method === "GET" || sanitizedRequest.method === "HEAD" ? null : sanitizedRequest.body,
      redirect: sanitizedRequest.redirect,
    });
    const gate = env.RELEASE_GATES.get(env.RELEASE_GATES.idFromName(appId));
    response = await gate.fetch(gatedRequest);
  } catch (error) {
    // Archival closes the gate and removes scripts but leaves the slug pointer,
    // since KV has no conditional delete. A missing selected Worker is absent.
    if (error instanceof Error && error.message.startsWith("Worker not found")) {
      return reject(404, "app_not_found", "route");
    }
    response = reject(502, "api_unavailable", "dispatch", appId);
  }
  // Responses from the API Worker are immutable; copy to add the identity.
  const responseHeaders = new Headers(response.headers);
  responseHeaders.set(RESPONSE_REQUEST_ID_HEADER, requestID);
  response = new Response(response.body, { status: response.status, statusText: response.statusText, headers: responseHeaders });
  const completedAt = Math.max(receivedAt, clock());
  completedRequestSink({
    event_id: `${requestID}:completed`,
    schema_version: 1,
    source: "edge",
    type: "request.completed",
    occurred_at: new Date(completedAt).toISOString(),
    app_id: appId,
    request_id: requestID,
    trace_id: requestID.slice("req_".length),
    received_at: new Date(receivedAt).toISOString(),
    completed_at: new Date(completedAt).toISOString(),
    method: request.method,
    path: url.pathname,
    status_code: response.status,
    duration_us: Math.round((completedAt - receivedAt) * 1_000),
    outcome: response.status >= 500 ? "failed" : "completed",
  });
  return response;
}

function emitDiagnostic(diagnostic: DispatcherDiagnostic): void {
  console.error(JSON.stringify(diagnostic));
}

async function resolveConfiguredApp(appSlug: string, env: DispatcherEnv): Promise<string | null> {
  if (RESERVED_APP_SLUGS.has(appSlug)) return null;
  try {
    const appID = await env.APP_ROUTES.get(appSlug, "text");
    assertAppId(appID);
    return appID;
  } catch {
    return null;
  }
}

function generateRequestID(): string {
  return `req_${crypto.randomUUID().replaceAll("-", "")}`;
}

function errorResponse(status: number, code: string): Response {
  return Response.json(
    { error: { code } },
    { status, headers: { "Cache-Control": "no-store" } },
  );
}
