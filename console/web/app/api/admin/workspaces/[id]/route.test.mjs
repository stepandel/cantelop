import { afterEach, expect, mock, test } from "bun:test";

import { ControlError } from "@core/domain/errors";

let admin = null;
let purge = async () => {};
let hostArchived = true;
const purged = [];
mock.module("@/lib/admin-session", () => ({ getAdminSession: async () => admin }));
mock.module("@core/db/database", () => ({ controlDatabase: () => ({}) }));
mock.module("@core/db/product-repository", () => ({
  ProductRepository: { fromPool: () => ({
    async purgeWorkspaceAsOperator(id) { purged.push(id); return purge(id); },
    async isWorkspaceHostArchived() { return hostArchived; },
  }) },
}));
mock.module("@core/workspace-host-archive", () => ({
  waitForWorkspaceHostArchive: async (products, id) => products.isWorkspaceHostArchived(id),
}));
const { DELETE } = await import("./route");
afterEach(() => { admin = null; purge = async () => {}; hostArchived = true; purged.length = 0; });
const id = "wsp_0123456789abcdef0123456789abcdef";
function request(origin = "https://cantelop.com") {
  return new Request(`https://cantelop.com/api/admin/workspaces/${id}`, {
    method: "DELETE", headers: { host: "cantelop.com", origin },
  });
}
const context = { params: Promise.resolve({ id }) };

test("rejects non-admins, cross-origin writes, and invalid IDs before queueing a purge", async () => {
  expect((await DELETE(request(), context)).status).toBe(401);
  admin = { user: { id: "admin" } };
  expect((await DELETE(request("https://other.example"), context)).status).toBe(400);
  expect((await DELETE(request(), { params: Promise.resolve({ id: "invalid" }) })).status).toBe(404);
  expect(purged).toEqual([]);
});

test("admin queues a Workspace purge; unknown Workspaces and outages are mapped", async () => {
  admin = { user: { id: "admin" } };
  expect((await DELETE(request(), context)).status).toBe(204);
  expect(purged).toEqual([id]);
  purge = async () => { throw new ControlError("not_found"); };
  expect((await DELETE(request(), context)).status).toBe(404);
  purge = async () => { throw new Error("database unavailable"); };
  const response = await DELETE(request(), context);
  expect(response.status).toBe(503);
  expect(await response.json()).toEqual({ error: "control_unavailable" });
});

test("admin purge reports pending while the compute host still resolves the Workspace", async () => {
  admin = { user: { id: "admin" } };
  hostArchived = false;
  const response = await DELETE(request(), context);
  expect(response.status).toBe(202);
  expect(response.headers.get("Retry-After")).toBe("1");
});
