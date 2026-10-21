import { describe, expect, test } from "bun:test";

import { EDGE_HEADERS, RESERVED_HEADERS, RUNTIME_HOSTNAME } from "@cantelop/edge-contracts";

import { dispatchPublicRequest } from "../dispatcher/src/core";
import { withMockReleaseGate } from "../dispatcher/test/mock-release-gate";
import { dispatchToOrigin, type OriginBrokerEnv } from "../origin-broker/src/core";
import { handleOutboundRequest } from "../outbound/src/core";

const appId = "app_0123456789abcdef0123456789abcdef";
const workspaceId = "wsp_0123456789abcdef0123456789abcdef";
const appSlug = "base-agent";
const hostname = "base-agent.cantelop.dev";
const requestId = "req_0123456789abcdef0123456789abcdef";
const messagePath = "/__cantelop/v1/messages";
const messageStatusPath =
  "/__cantelop/v1/sessions/agent%3Aprimary/messages/msg_0123456789abcdef0123456789abcdef";
const brokerEnv: OriginBrokerEnv = {
  ORIGIN_URL: "https://edge-origin.cantelop.dev",
  EDGE_INGRESS_TOKEN: "a".repeat(64),
  ORIGIN_ACCESS_CLIENT_ID: "broker.access",
  ORIGIN_ACCESS_CLIENT_SECRET: "access-secret",
};

describe("Cloudflare Worker chain", () => {
  test("streams one request through dispatcher, user API, outbound, and broker boundaries", async () => {
    let originRequest: Request | undefined;
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/customer/path?attempt=2`, {
        method: "PUT",
        headers: {
          Authorization: "Bearer end-user-token",
          "Content-Type": "application/octet-stream",
          [EDGE_HEADERS.appId]: "app_ffffffffffffffffffffffffffffffff",
          [EDGE_HEADERS.authorization]: "Bearer attacker-token",
        },
        body: "streamed payload",
      }),
      withMockReleaseGate({
        APP_ROUTES: { async get(slug) { return slug === appSlug ? appId : null; } },
        USER_WORKERS: {
          get: (_name, _args, options) => ({
            fetch: async (apiRequest) => {
              expect(await apiRequest.text()).toBe("streamed payload");
              return handleOutboundRequest(
                new Request(`https://${RUNTIME_HOSTNAME}${messagePath}`, {
                  method: "POST",
                  headers: {
                    Authorization: "Bearer end-user-token",
                    "Content-Type": "application/json",
                  },
                  body: JSON.stringify({
                    session: {
                      id: "agent:primary",
                      workspace_id: workspaceId,
                      keep_alive_seconds: 300,
                    },
                    message: {
                      id: "msg_0123456789abcdef0123456789abcdef",
                      payload: { prompt: "hello" },
                    },
                  }),
                }),
                {
                  CANTELOP_CONTEXT: options?.outbound?.CANTELOP_CONTEXT,
                  BROKER: {
                    dispatch: (brokerRequest, target) =>
                      dispatchToOrigin(brokerRequest, target, brokerEnv, async (request) => {
                        originRequest = request;
                        return new Response("workload response", { status: 207 });
                      }),
                  },
                },
              );
            },
          }),
        },
      }),
      () => requestId,
    );

    expect(response.status).toBe(207);
    expect(await response.text()).toBe("workload response");
    expect(originRequest?.url).toBe(`https://edge-origin.cantelop.dev${messagePath}`);
    expect(originRequest?.method).toBe("POST");
    expect(originRequest?.headers.get("Authorization")).toBe("Bearer end-user-token");
    expect(originRequest?.headers.get(EDGE_HEADERS.dispatch)).toBe("1");
    expect(originRequest?.headers.get(EDGE_HEADERS.authorization)).toBe(
      `Bearer ${brokerEnv.EDGE_INGRESS_TOKEN}`,
    );
    expect(originRequest?.headers.get(EDGE_HEADERS.requestId)).toBe(requestId);
    expect(originRequest?.headers.get(EDGE_HEADERS.appId)).toBe(appId);
    expect(originRequest?.headers.get(EDGE_HEADERS.workspaceId)).toBeNull();
    expect(originRequest?.headers.get(EDGE_HEADERS.hostname)).toBe(hostname);
    expect(originRequest?.headers.get("CF-Access-Client-Id")).toBe("broker.access");
    expect(originRequest?.headers.get("CF-Access-Client-Secret")).toBe("access-secret");
    expect(await originRequest?.json()).toEqual({
      session: {
        id: "agent:primary",
        workspace_id: workspaceId,
        keep_alive_seconds: 300,
      },
      message: {
        id: "msg_0123456789abcdef0123456789abcdef",
        payload: { prompt: "hello" },
      },
    });
  });

  test("preserves Message status lookup through the outbound and broker boundaries", async () => {
    let originRequest: Request | undefined;
    const response = await dispatchPublicRequest(
      new Request(`https://${hostname}/customer/status`),
      withMockReleaseGate({
        APP_ROUTES: { async get(slug) { return slug === appSlug ? appId : null; } },
        USER_WORKERS: {
          get: (_name, _args, options) => ({
            fetch: async () =>
              handleOutboundRequest(
                new Request(`https://${RUNTIME_HOSTNAME}${messageStatusPath}`, {
                  method: "GET",
                }),
                {
                  CANTELOP_CONTEXT: options?.outbound?.CANTELOP_CONTEXT,
                  BROKER: {
                    dispatch: (brokerRequest, target) =>
                      dispatchToOrigin(brokerRequest, target, brokerEnv, async (request) => {
                        originRequest = request;
                        return Response.json({
                          id: "msg_0123456789abcdef0123456789abcdef",
                          status: "handled",
                        });
                      }),
                  },
                },
              ),
          }),
        },
      }),
      () => requestId,
    );

    expect(response.status).toBe(200);
    expect(originRequest?.url).toBe(`https://edge-origin.cantelop.dev${messageStatusPath}`);
    expect(originRequest?.method).toBe("GET");
    expect(await originRequest?.text()).toBe("");
    expect(originRequest?.headers.get(EDGE_HEADERS.requestId)).toBe(requestId);
    expect(originRequest?.headers.get(EDGE_HEADERS.appId)).toBe(appId);
    expect(await response.json()).toEqual({
      id: "msg_0123456789abcdef0123456789abcdef",
      status: "handled",
    });
  });

  test("stops an unconfigured App at the dispatcher boundary", async () => {
    let originCalls = 0;
    const otherHostname = "other-app.cantelop.dev";
    const response = await dispatchPublicRequest(
      new Request(`https://${otherHostname}/`),
      withMockReleaseGate({
        APP_ROUTES: { async get(slug) { return slug === appSlug ? appId : null; } },
        USER_WORKERS: {
          get: () => ({
            fetch: async () => {
              originCalls += 1;
              return new Response();
            },
          }),
        },
      }),
      () => requestId,
    );

    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: { code: "app_not_found" } });
    expect(originCalls).toBe(0);
  });

  test("keeps every edge transport header reserved end to end", () => {
    expect(RESERVED_HEADERS).toHaveLength(6);
  });
});
