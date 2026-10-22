import { test, expect } from "bun:test";
import { synchronizeTailConsumers } from "./sync-tail-settings";

const consumers = [{ service: "cantelop-edge-tail" }];
const account = "a".repeat(32);

test("attaches through script settings and requires a separate readback", async () => {
  const calls: Array<{ url: string; method: string; body?: string }> = [];
  const request = (async (url, options) => {
    calls.push({ url: String(url), method: options?.method ?? "GET", body: options?.body as string });
    return Response.json(calls.length === 1 ? { success: true } :
      { success: true, result: { tail_consumers: consumers } });
  }) as typeof fetch;
  await synchronizeTailConsumers("cantelop-dispatcher", consumers, account, "private-token", request);
  expect(calls[0].url).toEndWith("/cantelop-dispatcher/script-settings");
  expect(calls[0].method).toBe("PATCH");
  expect(JSON.parse(calls[0].body!)).toEqual({ tail_consumers: consumers });
  expect(calls[1].url).toEndWith("/cantelop-dispatcher/settings");
  expect(calls[1].method).toBe("GET");
});

test("stops deployment when Cloudflare rejects the settings update", async () => {
  const request = (async () => Response.json({ success: false }, { status: 403 })) as typeof fetch;
  await expect(synchronizeTailConsumers("cantelop-dispatcher", consumers, account, "private-token", request))
    .rejects.toThrow("HTTP 403");
});

test("stops deployment when an accepted update loses the tail attachment", async () => {
  const request = (async () => Response.json({ success: true, result: { tail_consumers: [] } })) as typeof fetch;
  await expect(synchronizeTailConsumers("cantelop-dispatcher", consumers, account, "private-token", request, async () => {}))
    .rejects.toThrow("readback mismatch");
});


test("verifies a propagated attachment after an initially stale read", async () => {
  let calls = 0;
  const request = (async () => {
    calls++;
    return Response.json(calls === 1 ? { success: true } :
      { success: true, result: { tail_consumers: calls === 2 ? [] : consumers } });
  }) as typeof fetch;
  await synchronizeTailConsumers("cantelop-dispatcher", consumers, account, "private-token", request, async () => {});
  expect(calls).toBe(3);
});

test("readback authorization failures are not treated as propagation lag", async () => {
  let calls = 0;
  const request = (async () => ++calls === 1 ? Response.json({ success: true }) :
    Response.json({ success: false }, { status: 403 })) as typeof fetch;
  await expect(synchronizeTailConsumers("cantelop-dispatcher", consumers, account, "private-token", request, async () => {}))
    .rejects.toThrow("HTTP 403");
  expect(calls).toBe(2);
});
