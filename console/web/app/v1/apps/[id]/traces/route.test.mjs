import { afterEach, expect, mock, test } from "bun:test";
import { generateKeyPairSync } from "node:crypto";
import { tenantAccessToken } from "@core/auth/tokens";
import { ControlError } from "@core/domain/errors";

let ownerError;
let sourceError;
let reads = [];
const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";
mock.module("@core/product-control", () => ({ productControl: () => ({ assertActiveUser: async (id) => ({ id }),
  async getApp(owner, app) {
    expect([owner, app]).toEqual([userID, appID]);
    if (ownerError) throw ownerError;
  },
}) }));
mock.module("@core/observability/control", () => ({ observationRepository: () => ({
  async traceTree(id) {
    reads.push(["traces", id]);
    if (sourceError) throw sourceError;
    return { app_id: id, requests: [], unlinked_messages: [] };
  },
}) }));
mock.module("@/lib/customer-telemetry", () => ({
  async getCustomerSnapshot(owner, scope) {
    reads.push(["sandboxes", owner, scope]);
    if (sourceError) throw sourceError;
    return { ready: true, observed_at: "2026-09-11T12:00:00Z", sandboxes: [], sessions: [], workspaces: [] };
  },
}));
const { GET: traces } = await import("./route");
const { GET: sandboxes } = await import("../sandboxes/route");
const environment = { ...process.env };
afterEach(() => { process.env = { ...environment }; reads = []; ownerError = sourceError = undefined; });
const context = { params: Promise.resolve({ id: appID }) };
function request() {
  const { privateKey } = generateKeyPairSync("ed25519");
  process.env.NEXTAUTH_URL = "https://console.example.com";
  process.env.CANTELOP_CONTROL_PRIVATE_KEY = privateKey.export({ format: "der", type: "pkcs8" }).toString("base64url");
  return new Request("https://console.example.com", { headers: { Authorization: `Bearer ${tenantAccessToken(userID)}` } });
}
for (const [kind, get] of [["traces", traces], ["sandboxes", sandboxes]]) {
  test(`${kind} requires authentication before reading monitoring data`, async () => {
    expect((await get(new Request("https://console.example.com"), context)).status).toBe(401);
    expect(reads).toEqual([]);
  });
  test(`${kind} rejects invalid and unowned Apps before reading monitoring data`, async () => {
    expect((await get(request(), { params: Promise.resolve({ id: "invalid" }) })).status).toBe(404);
    ownerError = new ControlError("not_found");
    expect((await get(request(), context)).status).toBe(404);
    expect(reads).toEqual([]);
  });
  test(`${kind} uses the console source with App ownership and no caching`, async () => {
    const response = await get(request(), context);
    expect(response.status).toBe(200);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(reads).toEqual(kind === "traces" ? [["traces", appID]] : [["sandboxes", userID, { appID }]]);
    const payload = await response.json();
    expect(kind === "traces" ? payload.requests : payload.sandboxes).toEqual([]);
  });
  test(`${kind} reports monitoring outages without exposing internal errors`, async () => {
    sourceError = new Error("private database detail");
    const response = await get(request(), context);
    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({ error: "control_unavailable" });
  });
}
