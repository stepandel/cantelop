import {
  EDGE_HEADERS,
  PUBLIC_APP_DOMAIN,
  RESERVED_HEADERS,
  MESSAGE_ID_PATTERN,
  RUNTIME_HOSTNAME,
  WORKSPACE_ID_PATTERN,
  assertAPIInvocation,
  type APIInvocation,
  type DispatchTarget,
} from "@cantelop/edge-contracts";

const ACCESS_HEADERS = [
  "CF-Access-Client-Id",
  "CF-Access-Client-Secret",
] as const;
const WORKSPACE_CREATE_PATH = "/__cantelop/v1/workspaces";
const DATABASE_CREDENTIALS_PATH =
  "/__cantelop/v1/workspaces/database/credentials";
const WORKSPACE_OPEN_PATH = "/__cantelop/v1/workspaces/open";
const SESSION_CREATE_PATH = "/__cantelop/v1/sessions";
const SESSION_OPEN_PATH = "/__cantelop/v1/sessions/open";
const MESSAGE_PATH = "/__cantelop/v1/messages";
const REQUEST_PATH = "/__cantelop/v1/requests";
const SESSION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const WORKSPACE_SLUG_PATTERN = /^(?!.*--)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const BLOCKED_HOSTNAME_SUFFIXES = [
  ".internal",
  ".invalid",
  ".local",
  ".localhost",
  ".test",
  ".home.arpa",
] as const;

export interface OriginBrokerService {
  dispatch(request: Request, target: DispatchTarget): Promise<Response>;
}

export interface OutboundEnv {
  BROKER: OriginBrokerService;
  CANTELOP_CONTEXT?: unknown;
}

type PublicFetch = (request: Request) => Promise<Response>;

export async function handleOutboundRequest(
  request: Request,
  env: OutboundEnv,
  publicFetch: PublicFetch = (publicRequest) => fetch(publicRequest),
): Promise<Response> {
  const url = new URL(request.url);
  if (isRuntimeDestination(url)) {
    return dispatchRuntimeRequest(request, env);
  }
  if (!isAllowedPublicDestination(url)) {
    return errorResponse(403, "egress_denied");
  }

  const headers = sanitizedHeaders(request.headers);
  const publicRequest = cloneRequest(request, request.url, headers);
  try {
    return await publicFetch(publicRequest);
  } catch {
    return errorResponse(502, "egress_unavailable");
  }
}

function isRuntimeDestination(url: URL): boolean {
  return (
    url.protocol === "https:" &&
    url.hostname === RUNTIME_HOSTNAME &&
    url.username === "" &&
    url.password === "" &&
    url.port === ""
  );
}

async function dispatchRuntimeRequest(
  request: Request,
  env: OutboundEnv,
): Promise<Response> {
  const url = new URL(request.url);
  const integrationMethod = integrationRouteMethod(url);
  const workspaceRead =
    /^\/__cantelop\/v1\/workspaces\/wsp_[0-9a-f]{32}$/.test(url.pathname) &&
    !url.search;
  const databaseCredentials = url.pathname === DATABASE_CREDENTIALS_PATH;
  const environment = /^\/__cantelop\/v1\/environment\/(set|unset|status)$/.test(url.pathname);
  const workspaceCreate = url.pathname === WORKSPACE_CREATE_PATH;
  const workspaceOpen = url.pathname === WORKSPACE_OPEN_PATH;
  const sessionCreate = url.pathname === SESSION_CREATE_PATH;
  const sessionOpen = url.pathname === SESSION_OPEN_PATH;
  const messageAcceptance = url.pathname === MESSAGE_PATH;
  const sessionRequest = url.pathname === REQUEST_PATH;
  const sessionEvents = runtimeSessionEventsPath(url.pathname);
  const sessionTerminate = !sessionOpen && runtimeSessionPath(url.pathname);
  const messageStatus = runtimeMessageStatusPath(url.pathname);
  if (
    (!integrationMethod &&
      !workspaceRead &&
      !databaseCredentials &&
      !environment &&
      !workspaceCreate &&
      !workspaceOpen &&
      !sessionCreate &&
      !sessionOpen &&
      !messageAcceptance &&
      !sessionRequest &&
      !sessionEvents &&
      !sessionTerminate &&
      !messageStatus) ||
    (!integrationMethod &&
      !sessionEvents &&
      !messageStatus &&
      url.search !== "") ||
    (sessionEvents && !validSessionEventQuery(url.searchParams)) ||
    (messageStatus && !validMessageStatusQuery(url.searchParams))
  ) {
    return errorResponse(404, "runtime_route_not_found");
  }
  const expectedMethod =
    integrationMethod ??
    (workspaceRead
      ? "GET"
      : sessionTerminate
        ? "DELETE"
        : messageStatus || sessionEvents
          ? "GET"
          : "POST");
  if (request.method !== expectedMethod) {
    return Response.json(
      { error: { code: "runtime_method_not_allowed" } },
      {
        status: 405,
        headers: { Allow: expectedMethod, "Cache-Control": "no-store" },
      },
    );
  }

  let invocation: APIInvocation;
  try {
    assertAPIInvocation(env.CANTELOP_CONTEXT);
    invocation = env.CANTELOP_CONTEXT;
  } catch {
    return errorResponse(502, "invalid_dispatch_context");
  }

  const headers = sanitizedHeaders(request.headers);
  const brokerRequest = cloneRequest(request, request.url, headers);
  try {
    return await env.BROKER.dispatch(brokerRequest, invocation);
  } catch {
    return errorResponse(502, "broker_unavailable");
  }
}

function validSyncPath(value: string): boolean {
  const parts = value.split("/");
  return new TextEncoder().encode(value).length <= 1024 &&
    parts.length <= 32 &&
    !/[\\\x00\r\n]/.test(value) &&
    parts.every((part) => {
      const folded = part.toLowerCase();
      return !!part &&
        part !== "." &&
        part !== ".." &&
        !folded.startsWith(".cantelop-sync-") &&
        ![".git", ".cantelop", "node_modules", ".env"].includes(folded) &&
        !folded.startsWith(".env.");
    });
}

function integrationRouteMethod(url: URL): string | undefined {
  if (url.pathname === "/__cantelop/integration/v2/workspaces") {
    const query = url.searchParams;
    const ids = query.getAll("workspace_id"), slugs = query.getAll("workspace_slug");
    if ([...query.keys()].some(key => key !== "workspace_id" && key !== "workspace_slug")) return;
    if (ids.length === 1 && slugs.length === 0 && WORKSPACE_ID_PATTERN.test(ids[0]!) || slugs.length === 1 && ids.length === 0 && WORKSPACE_SLUG_PATTERN.test(slugs[0]!)) return "GET";
    return;
  }
  if (/^\/__cantelop\/integration\/v2\/workspaces\/wsp_[0-9a-f]{32}$/.test(url.pathname) && !url.search) return "DELETE";
  if (/^\/__cantelop\/integration\/v2\/workspaces\/wsp_[0-9a-f]{32}\/sync$/.test(url.pathname)) {
    const query = url.searchParams;
    for (const key of query.keys()) {
      if (!["op", "path", "base", "executable", "root"].includes(key) || query.getAll(key).length !== 1) return;
    }
    const op = query.get("op");
    const root = query.get("root");
    if (root !== null && root !== "" && !validSyncPath(root)) return;
    if (op === "scan") return ["path", "base", "executable"].some((key) => query.has(key)) ? undefined : "GET";
    const path = query.get("path") ?? "";
    if (!path || !validSyncPath(path) || !/^(missing|[0-9a-f]{64})$/.test(query.get("base") ?? "")) return;
    if (query.has("executable") && (op !== "write" || !/^[01]$/.test(query.get("executable") ?? ""))) return;
    return op === "read" ? "GET" : op === "write" ? "PUT" : op === "remove" ? "DELETE" : undefined;
  }
  const prefix = "/__cantelop/integration/v2/";
  if (
    (url.pathname === prefix + "messages" ||
      url.pathname === prefix + "requests") &&
    !url.search
  )
    return "POST";
  const match =
    /^\/__cantelop\/integration\/v2\/sessions\/([^/]+)(\/events|\/view|\/requests|\/messages\/msg_[0-9a-f]{32})?$/.exec(
      url.pathname,
    );
  if (!match) return;
  try {
    if (!SESSION_ID_PATTERN.test(decodeURIComponent(match[1]))) return;
  } catch {
    return;
  }
  const values = url.searchParams;
  const id = values.getAll("workspace_id"),
    slug = values.getAll("workspace_slug");
  if (
    (id.length === 1 &&
      slug.length === 0 &&
      WORKSPACE_ID_PATTERN.test(id[0])) ||
    (id.length === 0 &&
      slug.length === 1 &&
      WORKSPACE_SLUG_PATTERN.test(slug[0]))
  ) {
    for (const key of values.keys())
      if (
        (key !== "workspace_id" &&
          key !== "workspace_slug" &&
          (match[2] !== "/events" ||
            (key !== "after" && key !== "stream_id"))) ||
        values.getAll(key).length !== 1
      )
        return;
    if (match[2] === "/events") {
      const after = values.get("after"),
        stream = values.get("stream_id");
      if (
        (after !== null &&
          (!/^\d+$/.test(after) || !Number.isSafeInteger(Number(after)))) ||
        (stream !== null && !/^[0-9a-f]{32}$/.test(stream)) ||
        (after === null) !== (stream === null)
      )
        return;
    }
    return !match[2]
      ? "DELETE"
      : match[2] === "/requests"
        ? "POST"
        : "GET";
  }
}

function runtimeSessionEventsPath(pathname: string): boolean {
  const prefix = "/__cantelop/v1/sessions/";
  const suffix = "/events";
  if (!pathname.startsWith(prefix) || !pathname.endsWith(suffix)) return false;
  const encoded = pathname.slice(prefix.length, -suffix.length);
  if (encoded.length === 0 || encoded.includes("/")) return false;
  try {
    return SESSION_ID_PATTERN.test(decodeURIComponent(encoded));
  } catch {
    return false;
  }
}

function validSessionEventQuery(values: URLSearchParams): boolean {
  const workspaceIDs = values.getAll("workspace_id");
  const afterValues = values.getAll("after");
  const streamIDs = values.getAll("stream_id");
  if (
    [...values.keys()].some(
      (key) => key !== "workspace_id" && key !== "after" && key !== "stream_id",
    ) ||
    workspaceIDs.length !== 1 ||
    workspaceIDs[0] === "" ||
    afterValues.length > 1 ||
    streamIDs.length > 1 ||
    streamIDs[0] === ""
  )
    return false;
  return afterValues.length === 0 || /^\d+$/.test(afterValues[0] ?? "");
}

// SDK 0.14 scopes Message status reads to a Workspace, which the gateway
// requires once the durable mailbox is enabled; older SDKs send no query.
function validMessageStatusQuery(values: URLSearchParams): boolean {
  const workspaceIDs = values.getAll("workspace_id");
  if ([...values.keys()].some((key) => key !== "workspace_id")) return false;
  return (
    workspaceIDs.length === 0 ||
    (workspaceIDs.length === 1 && workspaceIDs[0] !== "")
  );
}

function runtimeSessionPath(pathname: string): boolean {
  const prefix = "/__cantelop/v1/sessions/";
  if (!pathname.startsWith(prefix)) return false;
  const encoded = pathname.slice(prefix.length);
  if (encoded.length === 0 || encoded.includes("/")) return false;
  try {
    return SESSION_ID_PATTERN.test(decodeURIComponent(encoded));
  } catch {
    return false;
  }
}

function runtimeMessageStatusPath(pathname: string): boolean {
  const prefix = "/__cantelop/v1/sessions/";
  if (!pathname.startsWith(prefix)) return false;
  const remainder = pathname.slice(prefix.length);
  const separator = remainder.indexOf("/messages/");
  if (separator < 1) return false;
  const encodedSession = remainder.slice(0, separator);
  const message = remainder.slice(separator + "/messages/".length);
  if (
    encodedSession.includes("/") ||
    message.includes("/") ||
    !MESSAGE_ID_PATTERN.test(message)
  ) {
    return false;
  }
  try {
    return SESSION_ID_PATTERN.test(decodeURIComponent(encodedSession));
  } catch {
    return false;
  }
}

function isAllowedPublicDestination(url: URL): boolean {
  if (
    url.protocol !== "https:" ||
    url.username !== "" ||
    url.password !== "" ||
    url.port !== ""
  ) {
    return false;
  }

  const hostname = url.hostname.toLowerCase();
  if (
    hostname.endsWith(".") ||
    hostname === PUBLIC_APP_DOMAIN ||
    hostname.endsWith(`.${PUBLIC_APP_DOMAIN}`) ||
    hostname === "localhost" ||
    BLOCKED_HOSTNAME_SUFFIXES.some((suffix) => hostname.endsWith(suffix))
  ) {
    return false;
  }

  return !isBlockedIPAddress(hostname);
}

function isBlockedIPAddress(hostname: string): boolean {
  const bareHostname =
    hostname.startsWith("[") && hostname.endsWith("]")
      ? hostname.slice(1, -1)
      : hostname;
  if (bareHostname.includes(":")) {
    return isBlockedIPv6(bareHostname);
  }

  const octets = bareHostname.split(".");
  if (octets.length !== 4 || octets.some((octet) => !/^\d{1,3}$/.test(octet))) {
    return false;
  }
  const values = octets.map(Number);
  if (values.some((value) => value > 255)) {
    return true;
  }
  const [first, second, third] = values;
  return (
    first === 0 ||
    first === 10 ||
    first === 127 ||
    (first === 100 && second >= 64 && second <= 127) ||
    (first === 169 && second === 254) ||
    (first === 172 && second >= 16 && second <= 31) ||
    (first === 192 && second === 0 && third === 0) ||
    (first === 192 && second === 0 && third === 2) ||
    (first === 192 && second === 88 && third === 99) ||
    (first === 192 && second === 168) ||
    (first === 198 && (second === 18 || second === 19)) ||
    (first === 198 && second === 51 && third === 100) ||
    (first === 203 && second === 0 && third === 113) ||
    first >= 224
  );
}

function isBlockedIPv6(address: string): boolean {
  const normalized = address.toLowerCase();
  if (normalized === "::" || normalized === "::1") {
    return true;
  }
  if (
    normalized.startsWith("fc") ||
    normalized.startsWith("fd") ||
    /^fe[89ab]/.test(normalized) ||
    normalized.startsWith("ff") ||
    normalized.startsWith("2001:db8:")
  ) {
    return true;
  }

  const mappedMatch = normalized.match(
    /^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/,
  );
  if (mappedMatch === null) {
    return false;
  }
  const high = Number.parseInt(mappedMatch[1], 16);
  const low = Number.parseInt(mappedMatch[2], 16);
  return isBlockedIPAddress(
    `${high >> 8}.${high & 0xff}.${low >> 8}.${low & 0xff}`,
  );
}

function sanitizedHeaders(headers: Headers): Headers {
  const sanitized = new Headers(headers);
  for (const header of [...RESERVED_HEADERS, ...ACCESS_HEADERS]) {
    sanitized.delete(header);
  }
  return sanitized;
}

function cloneRequest(
  request: Request,
  url: string,
  headers: Headers,
): Request {
  const body =
    request.method === "GET" || request.method === "HEAD" ? null : request.body;
  return new Request(url, {
    method: request.method,
    headers,
    body,
    redirect: "manual",
  });
}

function errorResponse(status: number, code: string): Response {
  return Response.json(
    { error: { code } },
    { status, headers: { "Cache-Control": "no-store" } },
  );
}
