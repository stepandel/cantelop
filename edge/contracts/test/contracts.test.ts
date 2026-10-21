import { describe, expect, test } from "bun:test";

import {
  EDGE_HEADERS,
  RESERVED_HEADERS,
  appSlugFromHostname,
  assertAPIInvocation,
  assertDispatchTarget,
  publicAppHostname,
} from "../src";

const appId = "app_0123456789abcdef0123456789abcdef";
const requestId = "req_0123456789abcdef0123456789abcdef";
const appSlug = "base-agent";
const hostname = "base-agent.cantelop.dev";

describe("public App hostname", () => {
  test("round trips the public App slug", () => {
    expect(publicAppHostname(appSlug)).toBe(hostname);
    expect(appSlugFromHostname(hostname)).toBe(appSlug);
  });

  test.each([
    "base_agent.cantelop.dev",
    "Base-agent.cantelop.dev",
    "workspace--base-agent.cantelop.dev",
    `${hostname}.`,
    `${hostname}:443`,
    `extra.${hostname}`,
    "cantelop.dev",
  ])("rejects noncanonical hostname %s", (candidate) => {
    expect(() => appSlugFromHostname(candidate)).toThrow("invalid public App hostname");
  });
});

describe("edge contracts", () => {
  const invocation = { request_id: requestId, app_id: appId, hostname };

  test("accepts a matching dispatcher invocation", () => {
    expect(() => assertAPIInvocation(invocation)).not.toThrow();
  });

  test("validates the App ID independently from its routed slug", () => {
    expect(() =>
      assertAPIInvocation({
        ...invocation,
        app_id: "app_ffffffffffffffffffffffffffffffff",
      }),
    ).not.toThrow();
    expect(() => assertAPIInvocation({ ...invocation, app_id: "app_invalid" })).toThrow(
      "invalid App ID",
    );
  });

  test("accepts an App-scoped dispatch target", () => {
    expect(() => assertDispatchTarget(invocation)).not.toThrow();
  });

  test("reserves only edge-scoped transport headers", () => {
    expect(RESERVED_HEADERS).toEqual([
      EDGE_HEADERS.dispatch,
      EDGE_HEADERS.authorization,
      EDGE_HEADERS.requestId,
      EDGE_HEADERS.appId,
      EDGE_HEADERS.workspaceId,
      EDGE_HEADERS.hostname,
    ]);
  });
});
