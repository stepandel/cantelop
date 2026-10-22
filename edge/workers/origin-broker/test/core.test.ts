import { describe, expect, test } from "bun:test";

import { EDGE_HEADERS } from "@cantelop/edge-contracts";

import { dispatchToOrigin, internalOnlyResponse, type OriginBrokerEnv } from "../src/core";

const target = {
  request_id: "req_0123456789abcdef0123456789abcdef",
  app_id: "app_0123456789abcdef0123456789abcdef",
  hostname: "base-agent.cantelop.dev",
};

const env: OriginBrokerEnv = {
  ORIGIN_URL: "https://edge-origin.cantelop.dev",
  EDGE_INGRESS_TOKEN: "a".repeat(64),
  ORIGIN_ACCESS_CLIENT_ID: "broker.access",
  ORIGIN_ACCESS_CLIENT_SECRET: "access-secret",
};

describe("origin broker", () => {
  test("forwards the request and replaces all trusted transport metadata", async () => {
    let forwarded: Request | undefined;
    const request = new Request(`https://${target.hostname}/hooks/run?attempt=2`, {
      method: "POST",
      headers: {
        Authorization: "Bearer end-user-token",
        [EDGE_HEADERS.authorization]: "Bearer attacker-token",
        [EDGE_HEADERS.workspaceId]: "wsp_ffffffffffffffffffffffffffffffff",
        [EDGE_HEADERS.computePool]: "managed",
      },
      body: "payload",
    });

    const response = await dispatchToOrigin(request, target, env, async (originRequest) => {
      forwarded = originRequest;
      return new Response("workload", { status: 201 });
    });

    expect(response.status).toBe(201);
    expect(await response.text()).toBe("workload");
    expect(forwarded?.url).toBe("https://edge-origin.cantelop.dev/hooks/run?attempt=2");
    expect(forwarded?.headers.get("Authorization")).toBe("Bearer end-user-token");
    expect(forwarded?.headers.get(EDGE_HEADERS.dispatch)).toBe("1");
    expect(forwarded?.headers.get(EDGE_HEADERS.computePool)).toBe("standard");
    expect(forwarded?.headers.get(EDGE_HEADERS.authorization)).toBe(`Bearer ${env.EDGE_INGRESS_TOKEN}`);
    expect(forwarded?.headers.get(EDGE_HEADERS.requestId)).toBe(target.request_id);
    expect(forwarded?.headers.get(EDGE_HEADERS.appId)).toBe(target.app_id);
    expect(forwarded?.headers.get(EDGE_HEADERS.workspaceId)).toBeNull();
    expect(forwarded?.headers.get(EDGE_HEADERS.hostname)).toBe(target.hostname);
    expect(forwarded?.headers.get("CF-Access-Client-Id")).toBe("broker.access");
    expect(forwarded?.headers.get("CF-Access-Client-Secret")).toBe("access-secret");
    expect(await forwarded?.text()).toBe("payload");
  });

  test("managed RPC replaces a forged pool with trusted managed placement", async () => {
    let forwarded: Request | undefined;
    await dispatchToOrigin(new Request(`https://${target.hostname}/`, {
      headers: { [EDGE_HEADERS.computePool]: "standard" },
    }), target, env, async (request) => { forwarded = request; return new Response(); }, "managed");
    expect(forwarded?.headers.get(EDGE_HEADERS.computePool)).toBe("managed");
    expect(forwarded?.headers.get(EDGE_HEADERS.authorization)).toBe(`Bearer ${env.EDGE_INGRESS_TOKEN}`);
  });

  test("removes the internal Access cookie while preserving customer cookies", async () => {
    const originHeaders = new Headers();
    originHeaders.append("Set-Cookie", "session=customer-value; Path=/; HttpOnly");
    originHeaders.append(
      "Set-Cookie",
      "CF_Authorization=internal-access-jwt; Path=/; Secure; HttpOnly",
    );
    originHeaders.set("X-Workload-Header", "preserved");

    const response = await dispatchToOrigin(
      new Request(`https://${target.hostname}/`),
      target,
      env,
      async () => new Response("workload", { status: 202, headers: originHeaders }),
    );

    expect(response.status).toBe(202);
    expect(response.headers.get("X-Workload-Header")).toBe("preserved");
    expect(response.headers.getSetCookie()).toEqual(["session=customer-value; Path=/; HttpOnly"]);
    expect(await response.text()).toBe("workload");
  });

  test("rejects an invalid API-provider result before origin fetch", async () => {
    let calls = 0;
    const response = await dispatchToOrigin(
      new Request(`https://${target.hostname}/`),
      { ...target, app_id: "app_invalid" },
      env,
      async () => {
        calls += 1;
        return new Response();
      },
    );

    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({ error: { code: "invalid_dispatch_target" } });
    expect(calls).toBe(0);
  });

  test.each([
    [{ ...env, ORIGIN_URL: "http://edge-origin.cantelop.dev" }],
    [{ ...env, ORIGIN_URL: "https://edge-origin.cantelop.dev/base" }],
    [{ ...env, EDGE_INGRESS_TOKEN: "short" }],
    [{ ...env, ORIGIN_ACCESS_CLIENT_ID: "" }],
    [{ ...env, ORIGIN_ACCESS_CLIENT_SECRET: undefined }],
  ])("fails closed for malformed configuration", async (candidate) => {
    const response = await dispatchToOrigin(
      new Request(`https://${target.hostname}/`),
      target,
      candidate,
      async () => new Response("unexpected"),
    );
    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({ error: { code: "broker_unavailable" } });
  });

  test("maps an origin transport failure without exposing details", async () => {
    const response = await dispatchToOrigin(
      new Request(`https://${target.hostname}/`),
      target,
      env,
      async () => {
        throw new Error("secret upstream detail");
      },
    );
    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({ error: { code: "origin_unavailable" } });
  });

  test("has no public fetch surface", async () => {
    const response = internalOnlyResponse();
    expect(response.status).toBe(404);
  });
});
