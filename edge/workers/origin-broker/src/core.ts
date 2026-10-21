import {
  EDGE_HEADERS,
  RESERVED_HEADERS,
  assertDispatchTarget,
  type DispatchTarget,
} from "@cantelop/edge-contracts";

const ACCESS_CLIENT_ID_HEADER = "CF-Access-Client-Id";
const ACCESS_CLIENT_SECRET_HEADER = "CF-Access-Client-Secret";
const ACCESS_AUTHORIZATION_COOKIE_PATTERN = /^\s*CF_Authorization\s*=/i;
const EDGE_TOKEN_PATTERN = /^[0-9a-f]{64}$/;

export interface OriginBrokerEnv {
  ORIGIN_URL?: string;
  EDGE_INGRESS_TOKEN?: string;
  ORIGIN_ACCESS_CLIENT_ID?: string;
  ORIGIN_ACCESS_CLIENT_SECRET?: string;
}

type OriginFetch = (request: Request) => Promise<Response>;

export async function dispatchToOrigin(
  request: Request,
  targetValue: unknown,
  env: OriginBrokerEnv,
  originFetch: OriginFetch = (originRequest) => fetch(originRequest),
): Promise<Response> {
  let target: DispatchTarget;
  try {
    assertDispatchTarget(targetValue);
    target = targetValue;
  } catch {
    return errorResponse(502, "invalid_dispatch_target");
  }

  const configuration = readConfiguration(env);
  if (configuration === null) {
    return errorResponse(503, "broker_unavailable");
  }

  const incomingURL = new URL(request.url);
  const originURL = new URL(configuration.originURL);
  originURL.pathname = incomingURL.pathname;
  originURL.search = incomingURL.search;

  const headers = new Headers(request.headers);
  for (const header of RESERVED_HEADERS) {
    headers.delete(header);
  }
  headers.set(EDGE_HEADERS.dispatch, "1");
  headers.set(EDGE_HEADERS.authorization, `Bearer ${configuration.edgeToken}`);
  headers.set(EDGE_HEADERS.requestId, target.request_id);
  headers.set(EDGE_HEADERS.appId, target.app_id);
  headers.set(EDGE_HEADERS.hostname, target.hostname);
  headers.set(ACCESS_CLIENT_ID_HEADER, configuration.accessClientId);
  headers.set(ACCESS_CLIENT_SECRET_HEADER, configuration.accessClientSecret);

  const body = request.method === "GET" || request.method === "HEAD" ? null : request.body;
  const originRequest = new Request(originURL, {
    method: request.method,
    headers,
    body,
    redirect: "manual",
  });

  try {
    return sanitizeOriginResponse(await originFetch(originRequest));
  } catch {
    return errorResponse(502, "origin_unavailable");
  }
}

function sanitizeOriginResponse(originResponse: Response): Response {
  const headers = new Headers(originResponse.headers);
  const cookies = originResponse.headers.getSetCookie();
  headers.delete("Set-Cookie");
  for (const cookie of cookies) {
    if (!ACCESS_AUTHORIZATION_COOKIE_PATTERN.test(cookie)) {
      headers.append("Set-Cookie", cookie);
    }
  }

  const webSocket = originResponse.webSocket;
  return new Response(webSocket == null ? originResponse.body : null, {
    status: originResponse.status,
    statusText: originResponse.statusText,
    headers,
    webSocket,
  });
}

export function internalOnlyResponse(): Response {
  return errorResponse(404, "not_found");
}

function readConfiguration(env: OriginBrokerEnv): {
  originURL: string;
  edgeToken: string;
  accessClientId: string;
  accessClientSecret: string;
} | null {
  if (
    typeof env.ORIGIN_URL !== "string" ||
    typeof env.EDGE_INGRESS_TOKEN !== "string" ||
    !EDGE_TOKEN_PATTERN.test(env.EDGE_INGRESS_TOKEN) ||
    typeof env.ORIGIN_ACCESS_CLIENT_ID !== "string" ||
    env.ORIGIN_ACCESS_CLIENT_ID.length === 0 ||
    typeof env.ORIGIN_ACCESS_CLIENT_SECRET !== "string" ||
    env.ORIGIN_ACCESS_CLIENT_SECRET.length === 0
  ) {
    return null;
  }

  try {
    const originURL = new URL(env.ORIGIN_URL);
    if (
      originURL.protocol !== "https:" ||
      originURL.username !== "" ||
      originURL.password !== "" ||
      originURL.pathname !== "/" ||
      originURL.search !== "" ||
      originURL.hash !== ""
    ) {
      return null;
    }
    return {
      originURL: originURL.href,
      edgeToken: env.EDGE_INGRESS_TOKEN,
      accessClientId: env.ORIGIN_ACCESS_CLIENT_ID,
      accessClientSecret: env.ORIGIN_ACCESS_CLIENT_SECRET,
    };
  } catch {
    return null;
  }
}

function errorResponse(status: number, code: string): Response {
  return Response.json(
    { error: { code } },
    { status, headers: { "Cache-Control": "no-store" } },
  );
}
