import { afterEach, expect, mock, test } from "bun:test";

let admin = null;
mock.module("@/lib/admin-session", () => ({ getAdminSession: async () => admin }));
mock.module("server-only", () => ({}));
const { DELETE, POST } = await import("./route");
const originalFetch = globalThis.fetch;
const environment = { ...process.env };
afterEach(() => { admin = null; globalThis.fetch = originalFetch; process.env = { ...environment }; });
const id = "sbx-0123456789abcdef0123456789abcdef";
function request(origin = "https://cantelop.com") {
  return new Request(`https://cantelop.com/api/admin/sandboxes/${id}`, {
    method: "DELETE", headers: { host: "cantelop.com", origin },
  });
}
const context = { params: Promise.resolve({ id }) };

test("rejects non-admins, cross-origin writes, and invalid IDs before touching the runtime", async () => {
  const fetch = mock(() => { throw new Error("unexpected runtime call"); });
  globalThis.fetch = fetch;
  expect((await DELETE(request(), context)).status).toBe(401);
  expect((await POST(new Request(`https://cantelop.com/api/admin/sandboxes/${id}`, {
    method: "POST", headers: { host: "cantelop.com", origin: "https://cantelop.com" },
  }), context)).status).toBe(401);
  admin = { user: { id: "admin" } };
  expect((await DELETE(request("https://other.example"), context)).status).toBe(400);
  expect((await POST(new Request(`https://cantelop.com/api/admin/sandboxes/${id}`, {
    method: "POST", headers: { host: "cantelop.com", origin: "https://other.example" },
  }), context)).status).toBe(400);
  expect((await DELETE(request(""), context)).status).toBe(400);
  expect((await DELETE(request(), { params: Promise.resolve({ id: "invalid" }) })).status).toBe(404);
  expect(fetch).not.toHaveBeenCalled();
});

test("admin can crash a sandbox; runtime errors are mapped without exposing credentials", async () => {
  admin = { user: { id: "admin" } };
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_RUNTIME_TOKEN = "r".repeat(32);
  let status = 200;
  globalThis.fetch = async (input, init) => {
    expect(String(input)).toBe(`https://status.example.com/sandboxes/${id}/crash`);
    expect(init.method).toBe("POST");
    return Response.json({ id, state: "failed", failure: { reason: "runtime_exit" } }, { status });
  };
  const crashRequest = () => new Request(`https://cantelop.com/api/admin/sandboxes/${id}`, {
    method: "POST", headers: { host: "cantelop.com", origin: "https://cantelop.com" },
  });
  const response = await POST(crashRequest(), context);
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual({ id, state: "failed", failure: { reason: "runtime_exit" } });
  for (const [upstream, expected, error] of [[401, 502, "sandbox_crash_auth_unavailable"], [404, 404, "not_found"], [409, 409, "invalid_state"], [500, 502, "sandbox_crash_unavailable"]]) {
    status = upstream;
    const failed = await POST(crashRequest(), context);
    expect(failed.status).toBe(expected);
    expect(await failed.json()).toEqual({ error });
  }
});

test("admin can stop a sandbox; runtime errors are mapped without exposing credentials", async () => {
  admin = { user: { id: "admin" } };
  process.env.FIRE_FUSE_OBSERVER_URL = "https://status.example.com";
  process.env.FIRE_FUSE_RUNTIME_TOKEN = "r".repeat(32);
  let status = 200;
  globalThis.fetch = async () => Response.json({ id, state: "terminated" }, { status });
  const response = await DELETE(request(), context);
  expect(response.status).toBe(200);
  expect(await response.json()).toEqual({ id, state: "terminated" });
  expect(response.headers.get("Cache-Control")).toBe("private, no-store");
  for (const [upstream, expected, error] of [[401, 502, "sandbox_stop_auth_unavailable"], [403, 502, "sandbox_stop_auth_unavailable"], [404, 404, "not_found"], [409, 409, "invalid_state"], [500, 502, "sandbox_stop_unavailable"]]) {
    status = upstream;
    const failed = await DELETE(request(), context);
    expect(failed.status).toBe(expected);
    expect(await failed.json()).toEqual({ error });
  }
});
