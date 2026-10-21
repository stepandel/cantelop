import { afterEach, expect, spyOn, test } from "bun:test";
import { watchRequestTrace } from "../lib/request-trace-polling";

const restores = [];
afterEach(() => {
  for (const spy of restores.splice(0)) spy.mockRestore();
});

function harness() {
  const callbacks = [];
  restores.push(spyOn(globalThis, "setTimeout").mockImplementation((callback) => {
    callbacks.push(callback);
    return callbacks.length;
  }));
  restores.push(spyOn(globalThis, "clearTimeout").mockImplementation(() => {}));
  let resolve;
  const response = new Promise((done) => { resolve = done; });
  const fetchSpy = spyOn(globalThis, "fetch").mockImplementation(() => response);
  restores.push(fetchSpy);
  return { callbacks, resolve, fetchSpy };
}

test("a cancelled in-flight trace cannot replace a newer exchange or schedule another poll", async () => {
  const { callbacks, resolve, fetchSpy } = harness();
  const updates = [];
  const stop = watchRequestTrace("app_test", "req_old", (...update) => updates.push(update));
  const pending = callbacks[0]();
  stop();
  const stopNew = watchRequestTrace("app_test", "req_new", (...update) => updates.push(update));
  expect(fetchSpy.mock.calls[0][1].signal.aborted).toBe(true);
  // Simulate a transport that completes despite abort; the old work is pending.
  resolve({ ok: true, json: async () => ({ requests: [{ id: "req_old", messages: [] }] }) });
  await pending;
  expect(updates).toEqual([]);
  expect(callbacks).toHaveLength(2);
  stopNew();
});

test("cleanup also ignores a response body that finishes after unmount", async () => {
  const { callbacks, resolve } = harness();
  const updates = [];
  let finishBody;
  let bodyStarted;
  const started = new Promise((done) => { bodyStarted = done; });
  const stop = watchRequestTrace("app_test", "req_old", (...update) => updates.push(update));
  const pending = callbacks[0]();
  resolve({ ok: true, json: () => { bodyStarted(); return new Promise((done) => { finishBody = done; }); } });
  await started;
  stop();
  finishBody({ requests: [{ id: "req_old", messages: [] }] });
  await pending;
  expect(updates).toEqual([]);
  expect(callbacks).toHaveLength(1);
});

test("an active exchange publishes its settled trace and stops polling", async () => {
  const { callbacks, resolve } = harness();
  const updates = [];
  const stop = watchRequestTrace("app_test", "req_new", (...update) => updates.push(update));
  const pending = callbacks[0]();
  const trace = { id: "req_new", messages: [{ attempts: [{ outcome: "completed" }] }] };
  resolve({ ok: true, json: async () => ({ requests: [trace] }) });
  await pending;
  expect(updates).toEqual([[trace, true]]);
  expect(callbacks).toHaveLength(1);
  stop();
});
