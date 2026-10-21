import { afterEach, expect, test } from "bun:test";

import { startPolling } from "../lib/polling";

const originals = { window: globalThis.window, document: globalThis.document };
afterEach(() => {
  globalThis.window = originals.window;
  globalThis.document = originals.document;
});

function harness() {
  const timers = new Map();
  const listeners = new Set();
  let next = 0;
  globalThis.window = {
    setInterval(callback, ms) { timers.set(++next, { callback, ms }); return next; },
    clearInterval(id) { timers.delete(id); },
  };
  globalThis.document = {
    hidden: false,
    addEventListener(type, listener) { if (type === "visibilitychange") listeners.add(listener); },
    removeEventListener(type, listener) { if (type === "visibilitychange") listeners.delete(listener); },
  };
  return {
    timers,
    listeners,
    tick() { for (const { callback } of timers.values()) callback(); },
    setHidden(hidden) {
      document.hidden = hidden;
      for (const listener of listeners) listener();
    },
  };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

test("polls on the interval but never overlaps a call still in flight", async () => {
  const page = harness();
  let calls = 0;
  let finish;
  const stop = startPolling(() => {
    calls += 1;
    return new Promise((resolve) => { finish = resolve; });
  }, 5_000);
  expect([...page.timers.values()].map(({ ms }) => ms)).toEqual([5_000]);
  expect(calls).toBe(0);
  page.tick();
  page.tick();
  expect(calls).toBe(1);
  finish();
  await settle();
  page.tick();
  expect(calls).toBe(2);
  stop();
  expect(page.timers.size).toBe(0);
  expect(page.listeners.size).toBe(0);
});

test("skips hidden ticks and polls as soon as the page is visible again", async () => {
  const page = harness();
  let calls = 0;
  const stop = startPolling(async () => { calls += 1; }, 5_000, { immediate: true });
  await settle();
  expect(calls).toBe(1);
  page.setHidden(true);
  page.tick();
  await settle();
  expect(calls).toBe(1);
  page.setHidden(false);
  await settle();
  expect(calls).toBe(2);
  stop();
});

test("stopping aborts the signal so a late response is discarded", async () => {
  harness();
  let signal;
  let finish;
  const stop = startPolling((received) => {
    signal = received;
    return new Promise((resolve) => { finish = resolve; });
  }, 5_000, { immediate: true });
  expect(signal.aborted).toBe(false);
  stop();
  expect(signal.aborted).toBe(true);
  finish();
});

test("a null interval loads once without scheduling or listening", async () => {
  const page = harness();
  let calls = 0;
  startPolling(async () => { calls += 1; }, null);
  await settle();
  expect(calls).toBe(1);
  expect(page.timers.size).toBe(0);
  expect(page.listeners.size).toBe(0);
});
