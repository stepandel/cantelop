import { expect, test } from "bun:test";

import dispatcher from "../src/index";

const appID = "app_0123456789abcdef0123456789abcdef";
const token = "a".repeat(64);
const url = `https://release-gate.cantelop.dev/__cantelop/release-gates/${appID}`;

test("release gate control requires its credential and forwards only bounded commands", async () => {
  const calls: Request[] = [];
  const env = {
    RELEASE_GATE_TOKEN: token,
    RELEASE_GATES: {
      idFromName(name: string) { expect(name).toBe(appID); return name as unknown as DurableObjectId; },
      get() { return { async fetch(request: Request) {
        calls.push(request);
        return Response.json({ app_id: appID, generation: "act_00000000000000000000000000000001", closed: true, script_name: null });
      } } as DurableObjectStub; },
    },
  };
  const context = { waitUntil() {} } as unknown as ExecutionContext;
  const send = (authorization: string | undefined, body: string) => dispatcher.fetch(new Request(url, {
    method: "PUT", headers: { "Content-Type": "application/json", ...(authorization ? { Authorization: authorization } : {}) },
    body,
  }), env as never, context);
  expect((await send(undefined, "{}")).status).toBe(401);
  expect((await send("Bearer wrong", "{}")).status).toBe(401);
  expect(calls).toHaveLength(0);
  expect((await send(`Bearer ${token}`, "x".repeat(513))).status).toBe(400);
  expect(calls).toHaveLength(0);
  expect((await send(`Bearer ${token}`, JSON.stringify({ action: "close", generation: "act_00000000000000000000000000000001", app_id: "app_fedcba9876543210fedcba9876543210" }))).status).toBe(200);
  expect(calls).toHaveLength(1);
  expect((await calls[0].json()).app_id).toBe(appID);
});
