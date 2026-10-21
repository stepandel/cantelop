import { expect, test } from "bun:test";

import { EdgeGateProvider } from "./edge-gate-provider";

const appID = `app_${"1".repeat(32)}`;
const releaseID = `rel_${"2".repeat(32)}`;
const generation = `act_${"0".repeat(31)}1`;
const token = "a".repeat(64);

test("authenticated control reads back the exact App gate identity", async () => {
  const calls = [];
  const provider = new EdgeGateProvider({ origin: "https://release-gate.cantelop.dev", token }, async (url, init) => {
    calls.push({ url, init });
    expect(init.headers.authorization).toBe(`Bearer ${token}`);
    if (init.method === "GET") return Response.json({ app_id: appID, generation, closed: false, script_name: releaseID });
    return Response.json({ app_id: appID, generation, closed: true, script_name: releaseID });
  });
  expect(await provider.close(appID, generation)).toMatchObject({ generation, closed: true, script_name: releaseID });
  expect(await provider.read(appID)).toMatchObject({ generation, closed: false, script_name: releaseID });
  expect(calls[0].url).toBe(`https://release-gate.cantelop.dev/__cantelop/release-gates/${appID}`);
  expect(JSON.parse(calls[0].init.body)).toEqual({ action: "close", generation });
});

test("rejects a wrong App receipt and an invalid script before selection", async () => {
  const provider = new EdgeGateProvider({ origin: "https://release-gate.cantelop.dev", token }, async () =>
    Response.json({ app_id: `app_${"3".repeat(32)}`, generation, closed: true, script_name: null }));
  await expect(provider.read(appID)).rejects.toThrow("invalid state");
  expect(() => provider.select(appID, generation, `app_${"z".repeat(32)}`)).toThrow(TypeError);
});
