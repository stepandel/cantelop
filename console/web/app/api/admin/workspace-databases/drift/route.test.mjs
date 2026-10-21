import { afterEach, expect, mock, spyOn, test } from "bun:test";

let admin = null;
let check = async () => ({ checked_at: "2026-10-05T12:00:00.000Z", provider_databases: 0, missing: [], orphaned: [] });
mock.module("@/lib/admin-session", () => ({ getAdminSession: async () => admin }));
mock.module("@core/db/database", () => ({ controlDatabase: () => ({}) }));
mock.module("@core/workspace-databases/drift", () => ({ checkWorkspaceDatabaseDrift: (...args) => check(...args) }));
const { GET } = await import("./route");
const environment = { ...process.env };
afterEach(() => { admin = null; process.env = { ...environment }; });
function configure() {
  Object.assign(process.env, {
    CANTELOP_TURSO_ENVIRONMENT: "staging", CANTELOP_TURSO_ORGANIZATION: "cantelop",
    CANTELOP_TURSO_GROUP: "staging", CANTELOP_TURSO_PLATFORM_TOKEN: "platform-token",
  });
}

test("requires an admin session", async () => {
  configure();
  expect((await GET()).status).toBe(401);
});

test("reports unconfigured providers and provider failures without details", async () => {
  // Failures are logged server-side under a short code; the body stays generic.
  const errorSpy = spyOn(console, "error").mockImplementation(() => {});
  try {
    admin = { user: { id: "admin" } };
    delete process.env.CANTELOP_TURSO_ENVIRONMENT;
    expect((await GET()).status).toBe(503);
    configure();
    check = async () => { throw new Error("platform-token leaked?"); };
    const response = await GET();
    expect(response.status).toBe(502);
    expect(await response.text()).not.toContain("platform-token");
    expect(errorSpy.mock.calls.map(([code]) => code)).toEqual(["database_provider_unconfigured", "drift_check_unavailable"]);
  } finally {
    errorSpy.mockRestore();
  }
});

test("returns the drift report uncached", async () => {
  admin = { user: { id: "admin" } };
  configure();
  check = async (_client, _provider, env) => ({ checked_at: "2026-10-05T12:00:00.000Z", provider_databases: 1, missing: [], orphaned: [{ database_name: `ct-${env}-x`, workspace_id: null, workspace_slug: null, app_slug: null }] });
  const response = await GET();
  expect(response.status).toBe(200);
  expect(response.headers.get("cache-control")).toBe("private, no-store");
  expect((await response.json()).orphaned[0].database_name).toBe("ct-staging-x");
});
