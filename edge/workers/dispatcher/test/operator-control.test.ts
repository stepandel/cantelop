import { expect, test } from "bun:test";
import dispatcher from "../src/index";
import { withMockReleaseGate } from "./mock-release-gate";

test("custom release-gate host requires authentication and never accepts the hosted alias", async () => {
  let calls = 0;
  const appID = "app_0123456789abcdef0123456789abcdef";
  const token = "a".repeat(64);
  const env = {
    ...withMockReleaseGate({
      CANTELOP_APP_DOMAIN: "agents.example.net", RELEASE_GATE_TOKEN: token,
      APP_ROUTES: { async get() { return null; } },
      USER_WORKERS: { get() { throw new Error("unexpected user Worker dispatch"); } },
    }),
    RELEASE_GATES: {
      idFromName(name: string) { expect(name).toBe(appID); return name as unknown as DurableObjectId; },
      get() { return { async fetch() { calls++; return Response.json({ ready: true }); } } as unknown as DurableObjectStub; },
    },
  };
  const context = { waitUntil() {} } as unknown as ExecutionContext;
  const url = `https://release-gate.agents.example.net/__cantelop/release-gates/${appID}`;
  expect((await dispatcher.fetch(new Request(url), env, context)).status).toBe(401);
  expect(calls).toBe(0);
  expect((await dispatcher.fetch(new Request(url, { headers: { Authorization: `Bearer ${token}` } }), env, context)).status).toBe(200);
  expect(calls).toBe(1);
  expect((await dispatcher.fetch(new Request(url.replace("agents.example.net", "cantelop.dev"), { headers: { Authorization: `Bearer ${token}` } }), env, context)).status).toBe(404);
  expect(calls).toBe(1);
});
