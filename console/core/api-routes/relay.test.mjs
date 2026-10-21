import { expect, test } from "bun:test";

import { ControlError } from "../domain/errors";
import { RELAY_MARKER_HEADER, RelayError, relayLimits, relayRequest, sendRelayRequest } from "./relay";

const declared = [{ method: "GET", path: "/health" }, { method: "POST", path: "/chat" }];
const origin = "https://vera.cantelop.dev";
const requestID = `req_${"a".repeat(32)}`;

test("accepts only a declared route, with bounded headers and a body where the method takes one", () => {
  expect(relayRequest({ method: "GET", path: "/health" }, declared)).toEqual({ method: "GET", path: "/health" });
  expect(relayRequest({
    method: "POST", path: "/chat", query: "workspace=default", body: '{"prompt":"hi"}',
    headers: { Authorization: "Bearer app-token", "X-Client": "console" },
  }, declared)).toEqual({
    method: "POST", path: "/chat", query: "workspace=default", body: '{"prompt":"hi"}',
    headers: { authorization: "Bearer app-token", "x-client": "console" },
  });
  expect(relayRequest({ method: "POST", path: "/chat", query: "", headers: {} }, declared)).toEqual({ method: "POST", path: "/chat" });

  for (const undeclared of [
    { method: "DELETE", path: "/health" },
    { method: "GET", path: "/health/" },
    { method: "GET", path: "/admin" },
    { method: "GET", path: "https://other.example/health" },
  ]) {
    expect(() => relayRequest(undeclared, declared)).toThrow(ControlError);
  }
  expect(() => relayRequest({ method: "GET", path: "/health" }, [])).toThrow(ControlError);

  for (const invalid of [
    null, [], "GET /health",
    { method: "GET", path: "/health", url: origin },
    { method: "GET", path: "/health", body: "{}" },
    { method: "POST", path: "/chat", body: "x".repeat(relayLimits.bodyBytes + 1) },
    { method: "GET", path: "/health", query: "a b" },
    { method: "GET", path: "/health", query: "a#b" },
    { method: "GET", path: "/health", headers: { Host: "evil.example" } },
    { method: "GET", path: "/health", headers: { "X-Cantelop-Edge-Dispatch": "1" } },
    { method: "GET", path: "/health", headers: { "X-Forwarded-For": "1.1.1.1" } },
    { method: "GET", path: "/health", headers: { Cookie: "session=1" } },
    { method: "GET", path: "/health", headers: { "Bad Name": "1" } },
    { method: "GET", path: "/health", headers: { "X-A": "line\nbreak" } },
    { method: "GET", path: "/health", headers: Object.fromEntries(Array.from({ length: relayLimits.headerCount + 1 }, (_, i) => [`X-H${i}`, "1"])) },
  ]) {
    expect(() => relayRequest(invalid, declared)).toThrow(TypeError);
  }
});

test("sends to the App's origin only, marks the request, and returns the response with its identity", async () => {
  let received;
  const fetchStub = async (url, init) => {
    received = { url, init, headers: Object.fromEntries(init.headers.entries()) };
    return new Response('{"ok":true}', { status: 202, headers: { "content-type": "application/json", "X-Cantelop-Request-ID": requestID, "cf-ray": "abc", "set-cookie": "a=b" } });
  };
  let clock = 1_000;
  const response = await sendRelayRequest(origin, {
    method: "POST", path: "/chat", query: "w=1", body: '{"prompt":"hi"}', headers: { authorization: "Bearer t" },
  }, { fetch: fetchStub, now: () => (clock += 40) });
  expect(received.url).toBe("https://vera.cantelop.dev/chat?w=1");
  expect(received.init.method).toBe("POST");
  expect(received.init.redirect).toBe("manual");
  expect(received.init.body).toBe('{"prompt":"hi"}');
  expect(received.headers).toEqual({ authorization: "Bearer t", "content-type": "application/json", [RELAY_MARKER_HEADER.toLowerCase()]: "1" });
  expect(response).toEqual({
    request_id: requestID,
    status: 202,
    headers: { "content-type": "application/json", "x-cantelop-request-id": requestID },
    body: '{"ok":true}',
    body_truncated: false,
    duration_ms: 40,
    sent: { method: "POST", url: "https://vera.cantelop.dev/chat?w=1", headers: received.headers },
  });

  for (const bad of ["http://vera.cantelop.dev", "https://vera.cantelop.dev/", "https://vera.example.com", "https://console.cantelop.dev.evil.example"]) {
    await expect(sendRelayRequest(bad, { method: "GET", path: "/health" }, { fetch: fetchStub })).rejects.toThrow(TypeError);
  }
});

test("bounds the response body and reports unreachable and timed-out Apps", async () => {
  const big = "x".repeat(relayLimits.responseBytes + 10);
  const truncated = await sendRelayRequest(origin, { method: "GET", path: "/health" }, { fetch: async () => new Response(big, { status: 200 }) });
  expect(truncated.body_truncated).toBe(true);
  expect(truncated.body.length).toBe(relayLimits.responseBytes);
  expect(truncated.request_id).toBeUndefined();

  await expect(sendRelayRequest(origin, { method: "GET", path: "/health" }, { fetch: async () => { throw new Error("ECONNREFUSED"); } }))
    .rejects.toMatchObject({ name: "RelayError", reason: "unreachable" });

  const hanging = (_url, init) => new Promise((_resolve, reject) => {
    init.signal.addEventListener("abort", () => reject(new Error("aborted")));
  });
  await expect(sendRelayRequest(origin, { method: "GET", path: "/health" }, { fetch: hanging, timeoutMs: 20 })).rejects.toMatchObject({ reason: "timeout" });
  expect(new RelayError("timeout")).toBeInstanceOf(Error);
});
