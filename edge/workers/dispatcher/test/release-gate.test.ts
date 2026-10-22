import { expect, test } from "bun:test";

import { GATE_APP_HEADER, GATE_INVOCATION_HEADER, ReleaseGate, type ReleaseGateState } from "../src/release-gate";

const appID = "app_0123456789abcdef0123456789abcdef";
const script = "rel_0123456789abcdef0123456789abcdef";
const generation = "act_0123456789abcdef0123456789abcdef";

function harness(invoke: (scriptName: string, request: Request) => Promise<Response>, exports: unknown = {}) {
  const stored = new Map<string, unknown>();
  let ready: Promise<unknown> = Promise.resolve();
  const state = {
    storage: {
      async get(key: string | string[]) {
        return Array.isArray(key) ? new Map(key.filter(name => stored.has(name)).map(name => [name, stored.get(name)])) : stored.get(key);
      },
      async put(key: string | Record<string, unknown>, value?: unknown) {
        if (typeof key === "string") stored.set(key, structuredClone(value));
        else for (const [name, entry] of Object.entries(key)) stored.set(name, structuredClone(entry));
      },
      async delete(keys: string | string[]) { for (const key of Array.isArray(keys) ? keys : [keys]) stored.delete(key); },
      async transaction<T>(callback: (storage: unknown) => Promise<T>) { return callback(this); },
    },
    exports,
    blockConcurrencyWhile(callback: () => Promise<unknown>) { ready = callback(); return ready; },
  };
  const env = { USER_WORKERS: { get(name: string) { return { fetch: (request: Request) => invoke(name, request) }; } } };
  const create = async () => {
    const gate = new ReleaseGate(state as unknown as DurableObjectState, env);
    await ready;
    return gate;
  };
  return { create };
}

function control(gate: ReleaseGate, action: "close" | "select" | "open", scriptName?: string) {
  return gate.fetch(new Request("https://release-gate.internal/", {
    method: "PUT", body: JSON.stringify({ app_id: appID, generation, action, ...(scriptName ? { script_name: scriptName } : {}) }),
  }));
}

function publicRequest() {
  return new Request("https://sample.cantelop.dev/api", { headers: {
    [GATE_APP_HEADER]: appID,
    [GATE_INVOCATION_HEADER]: JSON.stringify({
      request_id: "req_0123456789abcdef0123456789abcdef", app_id: appID, hostname: "sample.cantelop.dev",
    }),
  } });
}

test("release gate starts closed, selects an immutable script, and survives restart", async () => {
  const calls: string[] = [];
  const h = harness(async (name, request) => {
    calls.push(name);
    expect(request.headers.has(GATE_APP_HEADER)).toBe(false);
    expect(request.headers.has(GATE_INVOCATION_HEADER)).toBe(false);
    return new Response("active");
  });
  const gate = await h.create();
  expect((await gate.fetch(publicRequest())).status).toBe(503);
  expect((await control(gate, "close")).status).toBe(200);
  expect((await control(gate, "select", script)).status).toBe(200);
  expect((await control(gate, "open")).status).toBe(200);
  expect(await (await gate.fetch(publicRequest())).text()).toBe("active");
  expect(calls).toEqual([script]);
  const restarted = await h.create();
  expect(await (await restarted.fetch(publicRequest())).text()).toBe("active");
  expect(calls).toEqual([script, script]);
});

test("configuration rotation fences a late Fleet response before it can create a stale stream", async () => {
  let finish: (response: Response) => void = () => {};
  let started: () => void = () => {};
  const running = new Promise<void>(resolve => { started = resolve; });
  const response = new Promise<Response>(resolve => { finish = resolve; });
  const gate = await harness(async () => new Response(), { FleetRunner() { return { fetch() { started(); return response; } }; } }).create();
  await control(gate, "close");
  const configure = (revision: number, value: string) => gate.fetch(new Request("https://release-gate.internal/", {
    method: "PUT", body: JSON.stringify({ app_id: appID, action: "configure", revision, bindings: { SECRET: value } }),
  }));
  expect((await configure(1, "old")).status).toBe(200);
  expect((await control(gate, "select", `pkg_${"a".repeat(32)}`)).status).toBe(200);
  await control(gate, "open");
  const pending = gate.fetch(publicRequest());
  await running;
  expect((await configure(2, "current")).status).toBe(200);
  finish(new Response("stale private output"));
  const retired = await pending;
  expect(retired.status).toBe(503);
  expect(await retired.text()).not.toContain("stale private output");
});

test("closing the gate waits for admitted API work and rejects new requests", async () => {
  let finish: (response: Response) => void = () => {};
  const work = new Promise<Response>((resolve) => { finish = resolve; });
  const gate = await harness(async () => work).create();
  await control(gate, "close");
  await control(gate, "select", script);
  await control(gate, "open");
  const active = gate.fetch(publicRequest());
  let closed = false;
  const closing = control(gate, "close").then((response) => { closed = true; return response; });
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect((await gate.fetch(publicRequest())).status).toBe(503);
  expect(closed).toBe(false);
  finish(new Response("done"));
  expect(await (await active).text()).toBe("done");
  expect((await closing).status).toBe(200);
  expect((await gate.fetch(publicRequest())).status).toBe(503);
});

test("wrong generation cannot open a closed gate", async () => {
  const gate = await harness(async () => new Response()).create();
  await control(gate, "close");
  await control(gate, "select", script);
  const wrong = await gate.fetch(new Request("https://release-gate.internal/", {
    method: "PUT", body: JSON.stringify({ app_id: appID, generation: "act_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", action: "open" }),
  }));
  expect(wrong.status).toBe(409);
  expect((await gate.fetch(publicRequest())).status).toBe(503);
});

test("an older delayed close cannot replace a newer release selection", async () => {
  const gate = await harness(async () => new Response("new")).create();
  await control(gate, "close");
  await control(gate, "select", script);
  await control(gate, "open");
  const newer = "act_0123456789abcdef0123456789abcdf0";
  const update = await gate.fetch(new Request("https://release-gate.internal/", {
    method: "PUT", body: JSON.stringify({ app_id: appID, generation: newer, action: "close" }),
  }));
  expect(update.status).toBe(200);
  expect((await control(gate, "close")).status).toBe(409);
  expect((await control(gate, "open")).status).toBe(409);
  expect((await gate.fetch(publicRequest())).status).toBe(503);
});
