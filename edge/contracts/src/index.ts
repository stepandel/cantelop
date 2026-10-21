export const PUBLIC_APP_DOMAIN = "cantelop.dev";
export const RUNTIME_HOSTNAME = "runtime.cantelop.internal";

export const EDGE_HEADERS = {
  dispatch: "X-Cantelop-Edge-Dispatch",
  authorization: "X-Cantelop-Edge-Authorization",
  requestId: "X-Cantelop-Edge-Request-ID",
  appId: "X-Cantelop-Edge-App-ID",
  workspaceId: "X-Cantelop-Edge-Workspace-ID",
  hostname: "X-Cantelop-Edge-Hostname",
} as const;

/** Set on every dispatched response so a caller can find its request in the
 *  App's traces. The value is the observability identity, not a credential. */
export const RESPONSE_REQUEST_ID_HEADER = "X-Cantelop-Request-ID";

export const RESERVED_HEADERS = Object.freeze([
  EDGE_HEADERS.dispatch,
  EDGE_HEADERS.authorization,
  EDGE_HEADERS.requestId,
  EDGE_HEADERS.appId,
  EDGE_HEADERS.workspaceId,
  EDGE_HEADERS.hostname,
] as const);

export const APP_ID_PATTERN = /^app_[0-9a-f]{32}$/;
const APP_SLUG_PATTERN = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
export const REQUEST_ID_PATTERN = /^req_[0-9a-f]{32}$/;
export const WORKSPACE_ID_PATTERN = /^wsp_[0-9a-f]{32}$/;
export const MESSAGE_ID_PATTERN = /^msg_[0-9a-f]{32}$/;

export interface APIInvocation {
  request_id: string;
  app_id: string;
  hostname: string;
}

export type DispatchTarget = APIInvocation;

export class EdgeContractError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "EdgeContractError";
  }
}

export function assertAppId(appId: unknown): asserts appId is string {
  if (typeof appId !== "string" || !APP_ID_PATTERN.test(appId)) {
    throw new EdgeContractError("invalid App ID");
  }
}

export function assertAppSlug(appSlug: unknown): asserts appSlug is string {
  if (
    typeof appSlug !== "string" ||
    !APP_SLUG_PATTERN.test(appSlug) ||
    appSlug.includes("--")
  ) {
    throw new EdgeContractError("invalid App slug");
  }
}

export function assertPublicAppDomain(domain: unknown): asserts domain is string {
  if (typeof domain !== "string" || domain.length > 189 || domain.split(".").length < 2 ||
      domain.split(".").some((part) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(part)) || !/^[a-z]/.test(domain.split(".").at(-1)!)) {
    throw new EdgeContractError("invalid public App domain");
  }
}

export function publicAppHostname(appSlug: string, domain = PUBLIC_APP_DOMAIN): string {
  assertAppSlug(appSlug);
  assertPublicAppDomain(domain);
  return `${appSlug}.${domain}`;
}

export function appSlugFromHostname(hostname: string, domain = PUBLIC_APP_DOMAIN): string {
  assertPublicAppDomain(domain);
  const suffix = `.${domain}`;
  if (hostname.trim() !== hostname || !hostname.endsWith(suffix)) {
    throw new EdgeContractError("invalid public App hostname");
  }
  const label = hostname.slice(0, -suffix.length);
  try {
    assertAppSlug(label);
  } catch {
    throw new EdgeContractError("invalid public App hostname");
  }
  return label;
}

export function assertAPIInvocation(value: unknown, domain = PUBLIC_APP_DOMAIN): asserts value is APIInvocation {
  if (!isRecord(value)) {
    throw new EdgeContractError("API invocation must be an object");
  }
  const { request_id: requestId, app_id: appId, hostname } = value;
  if (typeof requestId !== "string" || !REQUEST_ID_PATTERN.test(requestId)) {
    throw new EdgeContractError("invalid edge request ID");
  }
  assertAppId(appId);
  if (typeof hostname !== "string") {
    throw new EdgeContractError("invalid public App hostname");
  }
  appSlugFromHostname(hostname, domain);
}

export function assertDispatchTarget(value: unknown, domain = PUBLIC_APP_DOMAIN): asserts value is DispatchTarget {
  assertAPIInvocation(value, domain);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export * from "./observability";
