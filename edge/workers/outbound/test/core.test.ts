import { describe, expect, test } from "bun:test";

import {
  EDGE_HEADERS,
  RUNTIME_HOSTNAME,
  type APIInvocation,
  type DispatchTarget,
} from "@cantelop/edge-contracts";

import { handleOutboundRequest, type OutboundEnv } from "../src/core";
// Shared with the gateway and local runtime contract in internal/runtimecontract.
import runtimeRoutes from "../../../../internal/runtimecontract/routes.json";

const context: APIInvocation = {
  request_id: "req_0123456789abcdef0123456789abcdef",
  app_id: "app_0123456789abcdef0123456789abcdef",
  hostname: "base-agent.cantelop.dev",
};
const workspaceId = "wsp_0123456789abcdef0123456789abcdef";
const messageStatusPath =
  "/__cantelop/v1/sessions/agent%3Aprimary/messages/msg_0123456789abcdef0123456789abcdef";

describe("outbound Worker", () => {
  test.each([
    ["GET", "/__cantelop/integration/v2/workspaces?workspace_slug=a&workspace_slug=b"],
    ["GET", `/__cantelop/integration/v2/workspaces?workspace_slug=a&workspace_id=${workspaceId}`],
    ["GET", "/__cantelop/integration/v2/workspaces?workspace_slug=a&app_id=forged"],
    ["DELETE", "/__cantelop/integration/v2/workspaces/a"],
    ["DELETE", `/__cantelop/integration/v2/workspaces/${workspaceId}?workspace_slug=replacement`],
  ])("rejects invalid lifecycle scope before broker dispatch: %s %s", async (method, path) => {
    let calls = 0;
    const result = await handleOutboundRequest(new Request(`https://${RUNTIME_HOSTNAME}${path}`, { method }), {
      CANTELOP_CONTEXT: context, BROKER: { async dispatch() { calls++; return new Response(); } },
    });
    expect(result.status).toBe(404);
    expect(calls).toBe(0);
  });

  test.each([
    "op=scan&root=../other",
    "op=scan&root=%2Fabsolute",
    "op=scan&root=one//two",
    "op=scan&root=.GIT",
    "op=scan&root=one&root=two",
    "op=scan&root=ok&path=a",
    "op=write&root=ok&path=.ENV&base=missing",
  ])("rejects malformed sync scope before broker dispatch: %s", async (query) => {
    let calls = 0;
    const response = await handleOutboundRequest(
      new Request(`https://${RUNTIME_HOSTNAME}/__cantelop/integration/v2/workspaces/${workspaceId}/sync?${query}`, { method: query.startsWith("op=write") ? "PUT" : "GET" }),
      { CANTELOP_CONTEXT: context, BROKER: { async dispatch() { calls++; return new Response(null, { status: 204 }); } } },
    );
    expect(response.status).toBe(404);
    expect(calls).toBe(0);
  });

  test("dispatches App-scoped Workspace creation without caller identity headers", async () => {
    let brokerRequest: Request | undefined;
    let brokerTarget: DispatchTarget | undefined;
    const response = await handleOutboundRequest(
      new Request(`https://${RUNTIME_HOSTNAME}/__cantelop/v1/workspaces`, {
        method: "POST",
        headers: { [EDGE_HEADERS.workspaceId]: workspaceId },
        body: JSON.stringify({ slug: "preview" }),
      }),
      {
        CANTELOP_CONTEXT: context,
        BROKER: {
          async dispatch(request, target) {
            brokerRequest = request;
            brokerTarget = target;
            return Response.json({ id: workspaceId }, { status: 201 });
          },
        },
      },
    );

    expect(response.status).toBe(201);
    expect(brokerTarget).toEqual(context);
    expect(brokerRequest?.headers.get(EDGE_HEADERS.workspaceId)).toBeNull();
  });

  test.each([
    ["POST", "/__cantelop/v1/environment/set"],
    ["POST", "/__cantelop/v1/environment/unset"],
    ["POST", "/__cantelop/v1/environment/status"],
    ["POST", "/__cantelop/v1/sessions"],
    ["POST", "/__cantelop/v1/workspaces/open"],
    ["POST", "/__cantelop/v1/workspaces/database/credentials"],
    ["POST", "/__cantelop/v1/sessions/open"],
    ["POST", "/__cantelop/v1/messages"],
    ["POST", "/__cantelop/v1/requests"],
    ["GET", messageStatusPath],
    ["GET", `${messageStatusPath}?workspace_id=${workspaceId}`],
    [
      "GET",
      `/__cantelop/v1/sessions/agent%3Aprimary/events?workspace_id=${workspaceId}&after=3`,
    ],
    [
      "GET",
      `/__cantelop/v1/sessions/agent%3Aprimary/events?workspace_id=${workspaceId}&after=3&stream_id=str_1`,
    ],
    ["DELETE", "/__cantelop/v1/sessions/ses_editor"],
    ["DELETE", "/__cantelop/v1/sessions/github%3Arepository"],
  ])("dispatches App-scoped Session route %s %s", async (method, path) => {
    let brokerTarget: DispatchTarget | undefined;
    const response = await handleOutboundRequest(
      new Request(`https://${RUNTIME_HOSTNAME}${path}`, { method }),
      {
        CANTELOP_CONTEXT: context,
        BROKER: {
          async dispatch(_request, target) {
            brokerTarget = target;
            return new Response(null, { status: 204 });
          },
        },
      },
    );
    expect(response.status).toBe(204);
    expect(brokerTarget).toEqual(context);
  });

  test.each(runtimeRoutes.routes.map((route) => [route.method, route.path]))(
    "dispatches shared runtime contract route %s %s",
    async (method, path) => {
      let brokerCalls = 0;
      const response = await handleOutboundRequest(
        new Request(`https://${RUNTIME_HOSTNAME}${path}`, { method }),
        {
          CANTELOP_CONTEXT: context,
          BROKER: {
            async dispatch() {
              brokerCalls += 1;
              return new Response(null, { status: 204 });
            },
          },
        },
      );
      expect(response.status).toBe(204);
      expect(brokerCalls).toBe(1);
    },
  );

  test.each(runtimeRoutes.rejects.map((route) => [route.method, route.path, route.status]))(
    "rejects shared runtime contract shape %s %s with %d",
    async (method, path, status) => {
      let brokerCalls = 0;
      const response = await handleOutboundRequest(
        new Request(`https://${RUNTIME_HOSTNAME}${path}`, { method }),
        {
          CANTELOP_CONTEXT: context,
          BROKER: {
            async dispatch() {
              brokerCalls += 1;
              return new Response(null, { status: 204 });
            },
          },
        },
      );
      expect(response.status).toBe(status);
      expect((await response.json()).error.code).toBe(
        status === 405 ? "runtime_method_not_allowed" : "runtime_route_not_found",
      );
      expect(brokerCalls).toBe(0);
    },
  );

  test("rejects missing or forged dispatcher context", async () => {
    let brokerCalls = 0;
    const response = await handleOutboundRequest(
      new Request(`https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions`, {
        method: "POST",
        headers: { [EDGE_HEADERS.workspaceId]: workspaceId },
      }),
      {
        CANTELOP_CONTEXT: { ...context, app_id: "app_forged" },
        BROKER: {
          dispatch: async () => {
            brokerCalls += 1;
            return new Response();
          },
        },
      },
    );

    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({
      error: { code: "invalid_dispatch_context" },
    });
    expect(brokerCalls).toBe(0);
  });

  test.each([
    [
      `https://${RUNTIME_HOSTNAME}${messageStatusPath}?debug=1`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}${messageStatusPath}?workspace_id=`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}${messageStatusPath}?workspace_id=${workspaceId}&workspace_id=${workspaceId}`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}${messageStatusPath}?workspace_id=${workspaceId}&debug=1`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/agent%3Aprimary/messages/`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/agent%2Fprimary/messages/msg_0123456789abcdef0123456789abcdef`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/agent%3Aprimary/messages/msg_0123456789abcdef0123456789abcdeF`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/agent%3Aprimary/messages/msg_0123456789abcdef0123456789abcdef/extra`,
      "GET",
      404,
      null,
    ],
    [`https://${RUNTIME_HOSTNAME}${messageStatusPath}`, "POST", 405, "GET"],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/thread/events?workspace_id=${workspaceId}&debug=1`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/thread/events?workspace_id=${workspaceId}&stream_id=`,
      "GET",
      404,
      null,
    ],
    [
      `https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions/thread/events?workspace_id=${workspaceId}&stream_id=a&stream_id=b`,
      "GET",
      404,
      null,
    ],
  ])(
    "rejects requests outside the runtime protocol: %s %s",
    async (url, method, status, allow) => {
      let brokerCalls = 0;
      const response = await handleOutboundRequest(
        new Request(url, {
          method,
          headers: { [EDGE_HEADERS.workspaceId]: workspaceId },
        }),
        {
          CANTELOP_CONTEXT: context,
          BROKER: {
            dispatch: async () => {
              brokerCalls += 1;
              return new Response();
            },
          },
        },
      );

      expect(response.status).toBe(status);
      expect(response.headers.get("Allow")).toBe(allow);
      expect(brokerCalls).toBe(0);
    },
  );

  test("forwards allowed public HTTPS requests without internal metadata", async () => {
    let publicRequest: Request | undefined;
    const response = await handleOutboundRequest(
      new Request("https://api.example.com/v1/items", {
        method: "PUT",
        headers: {
          Authorization: "Bearer customer-token",
          [EDGE_HEADERS.requestId]: "forged",
          "CF-Access-Client-Id": "forged",
        },
        body: "payload",
        redirect: "follow",
      }),
      brokerlessEnv(),
      async (forwardedRequest) => {
        publicRequest = forwardedRequest;
        return new Response(null, {
          status: 307,
          headers: { Location: "https://other.test/" },
        });
      },
    );

    expect(response.status).toBe(307);
    expect(publicRequest?.url).toBe("https://api.example.com/v1/items");
    expect(publicRequest?.redirect).toBe("manual");
    expect(publicRequest?.headers.get("Authorization")).toBe(
      "Bearer customer-token",
    );
    expect(publicRequest?.headers.get(EDGE_HEADERS.requestId)).toBeNull();
    expect(publicRequest?.headers.get("CF-Access-Client-Id")).toBeNull();
    expect(await publicRequest?.text()).toBe("payload");
  });

  test.each([
    "http://api.example.com/",
    "https://user:password@api.example.com/",
    "https://api.example.com:8443/",
    "https://cantelop.dev/",
    "https://console.cantelop.dev/",
    "https://console.cantelop.dev./",
    `http://${RUNTIME_HOSTNAME}/`,
    `https://${RUNTIME_HOSTNAME}:8443/`,
    "https://localhost/",
    "https://service.local/",
    "https://10.0.0.1/",
    "https://100.64.0.1/",
    "https://127.0.0.1/",
    "https://169.254.169.254/",
    "https://172.31.255.255/",
    "https://192.168.1.1/",
    "https://198.18.0.1/",
    "https://203.0.113.1/",
    "https://224.0.0.1/",
    "https://[::1]/",
    "https://[fd00::1]/",
    "https://[fe80::1]/",
    "https://[2001:db8::1]/",
    "https://[::ffff:127.0.0.1]/",
  ])("blocks non-public egress destination %s", async (url) => {
    let publicCalls = 0;
    const response = await handleOutboundRequest(
      new Request(url),
      brokerlessEnv(),
      async () => {
        publicCalls += 1;
        return new Response();
      },
    );

    expect(response.status).toBe(403);
    expect(await response.json()).toEqual({ error: { code: "egress_denied" } });
    expect(publicCalls).toBe(0);
  });

  test("maps broker and public network failures without exposing details", async () => {
    const brokerResponse = await handleOutboundRequest(
      new Request(`https://${RUNTIME_HOSTNAME}/__cantelop/v1/sessions`, {
        method: "POST",
        headers: { [EDGE_HEADERS.workspaceId]: workspaceId },
      }),
      {
        CANTELOP_CONTEXT: context,
        BROKER: {
          dispatch: async () => {
            throw new Error("secret broker detail");
          },
        },
      },
    );
    const egressResponse = await handleOutboundRequest(
      new Request("https://api.example.com/"),
      brokerlessEnv(),
      async () => {
        throw new Error("secret network detail");
      },
    );

    expect(brokerResponse.status).toBe(502);
    expect(await brokerResponse.json()).toEqual({
      error: { code: "broker_unavailable" },
    });
    expect(egressResponse.status).toBe(502);
    expect(await egressResponse.json()).toEqual({
      error: { code: "egress_unavailable" },
    });
  });
});

function brokerlessEnv(): OutboundEnv {
  return {
    BROKER: {
      dispatch: async () => {
        throw new Error("unexpected broker dispatch");
      },
    },
  };
}

describe("SDK 1.0 private transport", () => {
  const prefix = "/__cantelop/integration/v2/";
  test("routes v2 operations through the authoritative broker context", async () => {
    for (const [method, path] of [
      ["POST", prefix + "messages"],
      ["POST", prefix + "requests"],
      ["GET", "/__cantelop/v1/workspaces/" + workspaceId],
      ["GET", prefix + "sessions/thread/events?workspace_slug=production"],
      [
        "GET",
        prefix +
          "sessions/thread/messages/msg_" +
          "a".repeat(32) +
          "?workspace_id=" +
          workspaceId,
      ],
      ["DELETE", prefix + "sessions/thread?workspace_slug=production"],
      ["GET", prefix + "sessions/thread/view?workspace_id=" + workspaceId],
    ]) {
      let forwarded = false;
      const response = await handleOutboundRequest(
        new Request("https://" + RUNTIME_HOSTNAME + path, {
          method,
          headers: { [EDGE_HEADERS.appId]: "forged" },
        }),
        {
          CANTELOP_CONTEXT: context,
          BROKER: {
            async dispatch(request, target) {
              forwarded = true;
              expect(target).toEqual(context);
              expect(request.headers.get(EDGE_HEADERS.appId)).toBeNull();
              return Response.json({ ok: true });
            },
          },
        },
      );
      expect(response.status).toBe(200);
      expect(forwarded).toBe(true);
    }
  });
  test("rejects unscoped, ambiguous or malformed reads before broker invocation", async () => {
    const scope = "workspace_id=" + workspaceId;
    for (const path of [
      "sessions/thread/view",
      "sessions/thread/view?" + scope + "&workspace_slug=production",
      "sessions/thread/events?" + scope + "&after=1",
      "sessions/thread/events?" +
        scope +
        "&stream_id=" +
        "a".repeat(32) +
        "&after=9007199254740992",
      "sessions/thread?" + scope + "&unexpected=1",
      "sessions/thread/messages/msg_bad?" + scope,
      "sessions/thread/messages/msg_" + "a".repeat(32) + "/cancel?" + scope,
    ]) {
      const response = await handleOutboundRequest(
        new Request("https://" + RUNTIME_HOSTNAME + prefix + path),
        {
          CANTELOP_CONTEXT: context,
          BROKER: {
            async dispatch() {
              throw new Error("must not dispatch");
            },
          },
        },
      );
      expect(response.status).toBe(404);
    }
  });
});
