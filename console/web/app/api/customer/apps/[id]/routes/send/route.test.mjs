import { afterEach, expect, mock, test } from "bun:test";
import { NextRequest } from "next/server";
import { ControlError } from "@core/domain/errors";
import { RelayError } from "@core/api-routes/relay";

let customerSession;
let relay;
mock.module("@/lib/user-session", () => ({ getUserSession: async () => customerSession }));
mock.module("@core/api-routes/control", () => ({
  relayToApp: (...args) => relay(...args),
  appRoutes: () => { throw new Error("not this test"); },
  appRouteDetail: () => { throw new Error("not this test"); },
}));
const { POST } = await import("./route");
const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
const context = { params: Promise.resolve({ id: appID }) };
afterEach(() => { customerSession = undefined; relay = undefined; });

function request(body, origin = "https://console.cantelop.dev") {
  return new NextRequest(`https://console.cantelop.dev/api/customer/apps/${appID}/routes/send`, {
    method: "POST",
    headers: { host: "console.cantelop.dev", origin, "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

test("sending requires a session and the console's own origin", async () => {
  relay = () => { throw new Error("relayed without checks"); };
  expect((await POST(request({ method: "GET", path: "/health" }), context)).status).toBe(401);
  customerSession = { user: { id: userID } };
  expect((await POST(request({ method: "GET", path: "/health" }, "https://foreign.example"), context)).status).toBe(400);
});

test("relays the operator's request as the owner and returns the App's answer uncached", async () => {
  customerSession = { user: { id: userID } };
  relay = async (owner, app, input) => {
    expect([owner, app]).toEqual([userID, appID]);
    expect(input).toEqual({ method: "POST", path: "/chat", body: "{}" });
    return { status: 202, headers: {}, body: "", body_truncated: false, duration_ms: 12, request_id: `req_${"a".repeat(32)}`, sent: { method: "POST", url: "https://vera.cantelop.dev/chat", headers: {} } };
  };
  const response = await POST(request({ method: "POST", path: "/chat", body: "{}" }), context);
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
  expect((await response.json()).status).toBe(202);
});

test("maps undeclared routes, malformed requests, and an unanswering App", async () => {
  customerSession = { user: { id: userID } };
  relay = async () => { throw new ControlError("not_found"); };
  expect((await POST(request({ method: "GET", path: "/nope" }), context)).status).toBe(404);
  relay = async () => { throw new TypeError("invalid relay request"); };
  expect((await POST(request({ method: "GET", path: "/health", headers: { Host: "x" } }), context)).status).toBe(400);
  relay = async () => { throw new RelayError("timeout"); };
  const timeout = await POST(request({ method: "GET", path: "/health" }), context);
  expect(timeout.status).toBe(502);
  expect(await timeout.json()).toEqual({ error: "app_timeout" });
  relay = () => { throw new Error("read a non-JSON body"); };
  const notJSON = new NextRequest(`https://console.cantelop.dev/api/customer/apps/${appID}/routes/send`, {
    method: "POST", headers: { host: "console.cantelop.dev", origin: "https://console.cantelop.dev", "content-type": "text/plain" }, body: "x",
  });
  expect((await POST(notJSON, context)).status).toBe(400);
});
