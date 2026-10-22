import { afterEach, expect, mock, test } from "bun:test";
import { NextRequest } from "next/server";
import { ControlError } from "@core/domain/errors";
import { FleetError } from "@core/fleets/model";

let customerSession;
let repo;
mock.module("@/lib/user-session", () => ({ getUserSession: async () => customerSession }));
mock.module("@core/fleets/control", () => ({ fleetControl: () => repo }));
const { POST: createFleet } = await import("./route");
const { PATCH: updateFleet } = await import("./[id]/route");
const { POST: createPrincipal } = await import("./[id]/principals/route");
const { DELETE: revokePrincipal } = await import("./[id]/principals/[principal_id]/route");

const userID = "usr_0123456789abcdef0123456789abcdef";
const fleetID = "flt_0123456789abcdef0123456789abcdef";
const principalID = "fsp_0123456789abcdef0123456789abcdef";
const fleetContext = { params: Promise.resolve({ id: fleetID }) };
const principalContext = { params: Promise.resolve({ id: fleetID, principal_id: principalID }) };
const unused = new Proxy({}, { get: () => () => { throw new Error("reached the repository without checks"); } });
afterEach(() => { customerSession = undefined; repo = undefined; });

function request(path, method, body, origin = "https://console.cantelop.dev") {
  return new NextRequest(`https://console.cantelop.dev/api/customer/fleets${path}`, {
    method,
    headers: { host: "console.cantelop.dev", origin, ...(body === undefined ? {} : { "content-type": "application/json" }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

test("every Fleet write requires a session and the console's own origin", async () => {
  repo = unused;
  expect((await createFleet(request("", "POST", { name: "agents", max_apps: null }))).status).toBe(401);
  expect((await updateFleet(request(`/${fleetID}`, "PATCH", { max_apps: 5 }), fleetContext)).status).toBe(401);
  expect((await createPrincipal(request(`/${fleetID}/principals`, "POST", {}), fleetContext)).status).toBe(401);
  expect((await revokePrincipal(request(`/${fleetID}/principals/${principalID}`, "DELETE"), principalContext)).status).toBe(401);
  customerSession = { user: { id: userID } };
  const foreign = "https://foreign.example";
  expect((await createFleet(request("", "POST", { name: "agents", max_apps: null }, foreign))).status).toBe(400);
  expect((await updateFleet(request(`/${fleetID}`, "PATCH", { max_apps: 5 }, foreign), fleetContext)).status).toBe(400);
  expect((await revokePrincipal(request(`/${fleetID}/principals/${principalID}`, "DELETE", undefined, foreign), principalContext)).status).toBe(400);
});

test("creates a Fleet as the signed-in owner and maps the quota", async () => {
  customerSession = { user: { id: userID } };
  repo = {
    async create(owner, name, maxApps) {
      expect([owner, name, maxApps]).toEqual([userID, "agents", null]);
      return { id: fleetID, owner_user_id: owner, name, max_apps: maxApps, created_at: new Date() };
    },
  };
  const created = await createFleet(request("", "POST", { name: "agents", max_apps: null }));
  expect(created.status).toBe(201);
  expect(created.headers.get("cache-control")).toBe("private, no-store");
  expect(await created.json()).toEqual({ id: fleetID, name: "agents", max_apps: null });
  repo = { async create() { throw new ControlError("quota_exceeded"); } };
  const full = await createFleet(request("", "POST", { name: "agents", max_apps: 10 }));
  expect(full.status).toBe(429);
  expect(await full.json()).toEqual({ error: "quota_exceeded" });
  repo = unused;
  expect((await createFleet(request("", "POST", { name: "agents" }))).status).toBe(400);
  expect((await createFleet(request("", "POST", { name: "agents", max_apps: 1, owner_user_id: "usr_x" }))).status).toBe(400);
});

test("changes the cap of an owned Fleet only", async () => {
  customerSession = { user: { id: userID } };
  repo = {
    async updateCapacity(owner, fleet, maxApps) {
      expect([owner, fleet, maxApps]).toEqual([userID, fleetID, 25]);
      return { id: fleet, max_apps: maxApps };
    },
  };
  const updated = await updateFleet(request(`/${fleetID}`, "PATCH", { max_apps: 25 }), fleetContext);
  expect(updated.status).toBe(200);
  expect(await updated.json()).toEqual({ id: fleetID, max_apps: 25 });
  repo = { async updateCapacity() { throw new FleetError("not_found", 404); } };
  expect((await updateFleet(request(`/${fleetID}`, "PATCH", { max_apps: null }), fleetContext)).status).toBe(404);
});

test("returns a new principal's secret once and uncached, and revokes by id", async () => {
  customerSession = { user: { id: userID } };
  repo = {
    async principal(owner, fleet, input) {
      expect([owner, fleet]).toEqual([userID, fleetID]);
      expect(input.label).toBe("worker");
      expect(input.scopes).toEqual(["installations:read"]);
      expect(Date.parse(input.expiresAt) - Date.now()).toBeGreaterThan(29 * 86_400_000);
      return { id: principalID, token: `${principalID}.${"a".repeat(64)}`, expiresAt: input.expiresAt };
    },
    async revoke(owner, fleet, principal) {
      expect([owner, fleet, principal]).toEqual([userID, fleetID, principalID]);
    },
  };
  const created = await createPrincipal(
    request(`/${fleetID}/principals`, "POST", { label: "worker", scopes: ["installations:read"], expires_in_days: 30 }),
    fleetContext,
  );
  expect(created.status).toBe(201);
  expect(created.headers.get("cache-control")).toBe("private, no-store");
  expect((await created.json()).token).toStartWith(`${principalID}.`);
  const revoked = await revokePrincipal(request(`/${fleetID}/principals/${principalID}`, "DELETE"), principalContext);
  expect(await revoked.json()).toEqual({ revoked: true });
  repo = unused;
  expect((await createPrincipal(
    request(`/${fleetID}/principals`, "POST", { label: "worker", scopes: ["tenant:write"], expires_in_days: 30 }),
    fleetContext,
  )).status).toBe(400);
  const badID = { params: Promise.resolve({ id: fleetID, principal_id: "fsp_nope" }) };
  expect((await revokePrincipal(request(`/${fleetID}/principals/fsp_nope`, "DELETE"), badID)).status).toBe(400);
});
