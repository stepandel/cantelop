import { afterEach, expect, mock, test } from "bun:test";
import { ControlError } from "@core/domain/errors";
let authorized = true, hostArchived = true, failure;
const calls = [];
mock.module("@core/http/compute-auth", () => ({ authenticateGatewayProductControl: token => {
  expect(token).toBe("Bearer gateway-token"); if (!authorized) throw new Error("unauthorized");
} }));
mock.module("@core/product-control", () => ({ productControl: () => ({
  deleteWorkspaceForApp: async (app, workspace) => { calls.push([app, workspace]); if (failure) throw failure; return hostArchived; },
}) }));
const { DELETE } = await import("./route");
const id = "app_0123456789abcdef0123456789abcdef", workspace = "wsp_0123456789abcdef0123456789abcdef";
const request = () => new Request("https://cantelop.com/internal/v1/apps/"+id+"/workspaces/"+workspace, { method: "DELETE", headers: { authorization: "Bearer gateway-token" } });
const context = { params: Promise.resolve({ id, workspace }) };
afterEach(() => { authorized = true; hostArchived = true; failure = undefined; calls.length = 0; });
test("requires gateway authentication and canonical IDs before deletion", async () => {
  authorized = false;
  expect((await DELETE(request(), context)).status).toBe(401);
  authorized = true;
  expect((await DELETE(request(), { params: Promise.resolve({ id, workspace: "bad" }) })).status).toBe(400);
  expect(calls).toEqual([]);
});
test("passes both canonical identities to the scoped lifecycle and reports asynchronous purge", async () => {
  for (const archived of [true, false]) {
    hostArchived = archived;
    const result = await DELETE(request(), context);
    expect(result.status).toBe(202);
    expect(await result.json()).toEqual({ id: workspace, status: "deleting", hostArchived: archived });
    expect(result.headers.get("Cache-Control")).toBe("private, no-store");
  }
  expect(calls).toEqual([[id, workspace], [id, workspace]]);
});
test("foreign workspace and infrastructure errors retain existing mappings", async () => {
  failure = new ControlError("not_found");
  expect((await DELETE(request(), context)).status).toBe(404);
  failure = new Error("database unavailable");
  const result = await DELETE(request(), context);
  expect(result.status).toBe(503);
  expect(await result.json()).toEqual({ error: "control_unavailable" });
});
