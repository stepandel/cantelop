import { afterEach, expect, mock, spyOn, test } from "bun:test";
import { createHmac } from "node:crypto";
import { NextRequest } from "next/server";
import { DatabaseAccessError } from "@core/workspace-databases/service";
import { DatabaseProviderError } from "@core/workspace-databases/provider";

const identity = { sandboxId: "sbx-0123456789abcdef0123456789abcdef", appId: "app_0123456789abcdef0123456789abcdef", workspaceId: "wsp_0123456789abcdef0123456789abcdef" };
const key = "01".repeat(32);
const environment = { ...process.env };
let calls = [], failure;
const credentials = { url: "libsql://workspace.turso.io", authToken: "private-database-token", expiresAt: "2026-10-20T22:00:00Z" };
mock.module("@core/workspace-databases/configured", () => ({ workspaceDatabases: () => ({
  applicationCredentials: async (...args) => { calls.push(args); if (failure) throw failure; return credentials; },
}) }));
const { POST } = await import("./route");
let fetchSpy, warnSpy;
afterEach(() => {
  fetchSpy?.mockRestore(); warnSpy?.mockRestore();
  process.env = { ...environment }; calls = []; failure = undefined;
});
function configure(response = () => Response.json({ ...identity, active: true })) {
  Object.assign(process.env, { CANTELOP_WORKSPACE_DATABASE_CAPABILITY_KEY: key, FIRE_FUSE_OBSERVER_URL: "https://status.example.com", FIRE_FUSE_STATUS_TOKEN: "private-observer-token".repeat(2) });
  fetchSpy = spyOn(globalThis, "fetch").mockImplementation(response);
  warnSpy = spyOn(console, "warn").mockImplementation(() => {});
}
function request(token) {
  const body = Buffer.from(JSON.stringify(identity)).toString("base64url");
  const mac = createHmac("sha256", Buffer.from(key, "hex")).update(`cantelop-workspace-database-v1\0${body}`).digest("base64url");
  return new NextRequest("https://cantelop.com/internal/v1/runtime/database/credentials", { method: "POST", headers: { Authorization: token ?? `Bearer ${body}.${mac}` } });
}
test("unauthorized capabilities cannot query the observer or database", async () => {
  configure();
  expect((await POST(request("Bearer invalid"))).status).toBe(401);
  expect(fetchSpy).not.toHaveBeenCalled(); expect(calls).toEqual([]); expect(warnSpy).not.toHaveBeenCalled();
});
test("credentials are issued only after a matching current assignment is verified", async () => {
  configure();
  const response = await POST(request());
  expect(response.status).toBe(200); expect(await response.json()).toEqual(credentials);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(calls).toEqual([[identity.appId, identity.workspaceId]]); expect(warnSpy).not.toHaveBeenCalled();
});
test("verification failures cannot reach the database and log only safe diagnostics", async () => {
  configure(() => new Response("private-provider-body", { status: 503 }));
  const response = await POST(request());
  expect(response.status).toBe(503);
  expect(await response.json()).toEqual({ error: { code: "database_access_unavailable" } });
  expect(calls).toEqual([]);
  expect(warnSpy.mock.calls).toEqual([["runtime_database_credentials_failed", {
    stage: "sandbox_verification", code: "verification_http_error", status: 503,
    sandbox_id: identity.sandboxId, app_id: identity.appId, workspace_id: identity.workspaceId,
  }]]);
  expect(JSON.stringify(warnSpy.mock.calls)).not.toContain("private-");
});
test("database errors report their stage without logging raw errors or credentials", async () => {
  configure();
  for (const [error, code, status] of [
    [new Error("private-provider-body"), "database_credentials_unavailable", undefined],
    [new DatabaseAccessError("database_not_ready"), "database_not_ready", undefined],
    [new DatabaseProviderError(true, 429), "database_provider_unavailable", 429],
  ]) {
    failure = error;
    const response = await POST(request());
    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({ error: { code: "database_access_unavailable" } });
    expect(warnSpy.mock.calls.at(-1)[1]).toMatchObject({ stage: "database_credentials", code, status });
    expect(JSON.stringify(warnSpy.mock.calls)).not.toContain("private-");
  }
});
