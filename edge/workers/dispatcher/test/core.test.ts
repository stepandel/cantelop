import { describe, expect, test } from "bun:test";

import { EDGE_HEADERS, RESERVED_HEADERS, RESPONSE_REQUEST_ID_HEADER, type APIInvocation } from "@cantelop/edge-contracts";

import { dispatchPublicRequest, type DispatcherEnv } from "../src/core";
import { mockReleaseGates } from "./mock-release-gate";

const appId = "app_0123456789abcdef0123456789abcdef";
const appSlug = "base-agent";
const hostname = "base-agent.cantelop.dev";
const requestId = "req_0123456789abcdef0123456789abcdef";

function workspace(
  invoke: (request: Request, invocation: APIInvocation) => Promise<Response>,
): DispatcherEnv {
  const workers: DispatcherEnv["USER_WORKERS"] = {
    get: (_name, _args, options) => ({
      fetch: (request) => invoke(request, options?.outbound?.CANTELOP_CONTEXT as APIInvocation),
    }),
  };
  return {
    APP_ROUTES: { async get(slug, type) { return slug === appSlug && type === "text" ? appId : null; } },
    USER_WORKERS: workers,
    RELEASE_GATES: mockReleaseGates(workers),
  };
}

describe("public dispatcher", () => {
  test("derives the App, strips reserved headers, and invokes the API Worker", async () => {
    let receivedRequest: Request | undefined;
    let receivedInvocation: APIInvocation | undefined;
    const headers = new Headers({
      Authorization: "Bearer end-user-token",
      "X-Customer-Header": "customer-value",
    });
    for (const header of RESERVED_HEADERS) {
      headers.set(header, "attacker-value");
    }
    const request = new Request(`https://${hostname}/hooks/run?attempt=2`, {
      method: "POST",
      headers,
      body: "payload",
    });

    const response = await dispatchPublicRequest(
      request,
      workspace(async (apiRequest, invocation) => {
        receivedRequest = apiRequest;
        receivedInvocation = invocation;
        return new Response("api", { status: 201 });
      }),
      () => requestId,
    );

    expect(response.status).toBe(201);
    expect(response.headers.get(RESPONSE_REQUEST_ID_HEADER)).toBe(requestId);
    expect(await response.text()).toBe("api");
    expect(receivedInvocation).toEqual({ request_id: requestId, app_id: appId, hostname });
    expect(receivedRequest?.url).toBe(`https://${hostname}/hooks/run?attempt=2`);
    expect(receivedRequest?.headers.get("Authorization")).toBe("Bearer end-user-token");
    expect(receivedRequest?.headers.get("X-Customer-Header")).toBe("customer-value");
    for (const header of RESERVED_HEADERS) {
      if (header === EDGE_HEADERS.requestId) expect(receivedRequest?.headers.get(header)).toBe(requestId);
      else expect(receivedRequest?.headers.has(header)).toBe(false);
    }
    expect(await receivedRequest?.text()).toBe("payload");
  });

  test("emits one correlated completion observation after the API Worker settles", async () => {
    const completed: unknown[] = [];
    const ticks = [Date.parse("2026-09-01T20:00:00Z"), Date.parse("2026-09-01T20:00:00.012Z")];
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/slack/events?signature=secret`, { method: "POST", body: "event" }),
      workspace(async () => new Response("accepted", { status: 202 })),
      () => requestId,
      () => {},
      (observation) => completed.push(observation),
      () => ticks.shift()!,
    );

    expect(response.status).toBe(202);
    expect(completed).toEqual([{
      event_id: `${requestId}:completed`, schema_version: 1, source: "edge", type: "request.completed",
      occurred_at: "2026-09-01T20:00:00.012Z", app_id: appId, request_id: requestId,
      trace_id: requestId.slice(4), received_at: "2026-09-01T20:00:00.000Z",
      completed_at: "2026-09-01T20:00:00.012Z", method: "POST", path: "/slack/events",
      status_code: 202, duration_us: 12_000, outcome: "completed",
    }]);
  });

  test("observes API failures without delaying or replacing their response", async () => {
    const completed: unknown[] = [];
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/fail`),
      workspace(async () => { throw new Error("unavailable"); }),
      () => requestId,
      () => {},
      (observation) => completed.push(observation),
      () => 1_000,
    );

    expect(response.status).toBe(502);
    expect(completed).toHaveLength(1);
    expect(completed[0]).toMatchObject({ request_id: requestId, status_code: 502, outcome: "failed" });
  });

  test("dispatches by stable App ID with trusted outbound context", async () => {
    let receivedName = "";
    let receivedArgs: Record<string, unknown> | undefined;
    let receivedContext: unknown;
    const workers: DispatcherEnv["USER_WORKERS"] = {
      get: (name, args, options) => {
        receivedName = name;
        receivedArgs = args;
        receivedContext = options?.outbound?.CANTELOP_CONTEXT;
        return { fetch: async () => new Response("customer API") };
      },
    };
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      {
        APP_ROUTES: { async get() { return appId; } },
        USER_WORKERS: workers,
        RELEASE_GATES: mockReleaseGates(workers),
      },
      () => requestId,
    );

    expect(response.status).toBe(200);
    expect(receivedName).toBe(appId);
    expect(receivedArgs).toEqual({});
    expect(receivedContext).toEqual({ request_id: requestId, app_id: appId, hostname });
  });

  test.each([
    "cantelop.dev",
    "extra.base-agent.cantelop.dev",
    "base_agent.cantelop.dev",
    "workspace--base-agent.cantelop.dev",
  ])("returns an unknown route for noncanonical hostname %s", async (candidate) => {
    let calls = 0;
    const response = await dispatchPublicRequest(
      new Request(`https://${candidate}/`),
      workspace(async () => {
        calls += 1;
        return new Response();
      }),
      () => requestId,
    );
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: { code: "app_not_found" } });
    expect(calls).toBe(0);
  });

  test("returns an unknown route for an unconfigured valid slug", async () => {
    let calls = 0;
    const response = await dispatchPublicRequest(
      new Request("https://another-app.cantelop.dev/"),
      workspace(async () => {
        calls += 1;
        return new Response();
      }),
      () => requestId,
    );
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: { code: "app_not_found" } });
    expect(calls).toBe(0);
  });

  test.each(["console", "edge-origin", "release-gate", "www"])(
    "never dispatches the reserved App slug %s",
    async (reservedSlug) => {
      let calls = 0;
      const env = workspace(async () => {
        calls += 1;
        return new Response();
      });
      const response = await dispatchPublicRequest(
        new Request(`https://${reservedSlug}.cantelop.dev/`),
        env,
        () => requestId,
      );
      expect(response.status).toBe(404);
      expect(calls).toBe(0);
    },
  );

  test("fails closed when the registered App ID is malformed", async () => {
    let calls = 0;
    const env = workspace(async () => {
      calls += 1;
      return new Response();
    });
    env.APP_ROUTES = { async get() { return "app_invalid"; } };
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      env,
      () => requestId,
    );
    expect(response.status).toBe(404);
    expect(calls).toBe(0);
  });

  test("fails closed when the route registry is unavailable", async () => {
    let calls = 0;
    const env = workspace(async () => {
      calls += 1;
      return new Response();
    });
    env.APP_ROUTES = { async get() { throw new Error("KV unavailable"); } };
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      env,
      () => requestId,
    );
    expect(response.status).toBe(404);
    expect(calls).toBe(0);
  });

  test("fails before API invocation if request ID generation is invalid", async () => {
    let calls = 0;
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      workspace(async () => {
        calls += 1;
        return new Response();
      }),
      () => "invalid",
    );
    expect(response.status).toBe(500);
    expect(await response.json()).toEqual({ error: { code: "dispatcher_unavailable" } });
    expect(calls).toBe(0);
  });

  test("maps an API RPC failure without exposing details", async () => {
    const diagnostics: unknown[] = [];
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      workspace(async () => {
        throw new Error("internal binding detail");
      }),
      () => requestId,
      (diagnostic) => diagnostics.push(diagnostic),
    );
    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({ error: { code: "api_unavailable" } });
    expect(diagnostics).toEqual([{
      source: "cantelop_dispatcher",
      outcome: "failed",
      stage: "dispatch",
      status: 502,
      reason: "api_unavailable",
      request_id: requestId,
      hostname,
      method: "GET",
      path: "/",
      app_id: appId,
    }]);
  });

  test("treats a slug pointing at a deleted Worker as not found", async () => {
    const observations: unknown[] = [];
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      workspace(async () => {
        throw new Error(`Worker not found: ${appId}`);
      }),
      () => requestId,
      () => {},
      (observation) => observations.push(observation),
    );
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: { code: "app_not_found" } });
    expect(observations).toEqual([]);
  });

  test("generates a contract-valid request ID", async () => {
    let generated = "";
    await dispatchPublicRequest(
      new Request(`https://${hostname}/`),
      workspace(async (_request, invocation) => {
        generated = invocation.request_id;
        return new Response();
      }),
    );
    expect(generated).toMatch(/^req_[0-9a-f]{32}$/);
  });

  test("removes the edge authorization header specifically", async () => {
    let received: Request | undefined;
    await dispatchPublicRequest(
      new Request(`https://${hostname}/`, {
        headers: { [EDGE_HEADERS.authorization]: "Bearer attacker-token" },
      }),
      workspace(async (request) => {
        received = request;
        return new Response();
      }),
      () => requestId,
    );
    expect(received?.headers.has(EDGE_HEADERS.authorization)).toBe(false);
  });
});
