import { expect, mock, test } from "bun:test";

mock.module("server-only", () => ({}));

const userID = "usr_0123456789abcdef0123456789abcdef";
const appID = "app_0123456789abcdef0123456789abcdef";

function session(id, ownedAppID, createdAt) {
  return {
    id,
    workspace_id: `wsp_${id.padEnd(32, "0").slice(0, 32)}`,
    app_id: ownedAppID,
    build_id: `sha256:${"a".repeat(64)}`,
    keep_alive_seconds: 600,
    state: "idle",
    created_at: createdAt,
    updated_at: createdAt,
  };
}

test("lists an App's Sessions from the owned observer snapshot", async () => {
  const older = session("ses_older", appID, "2026-08-13T00:00:00.000Z");
  const newer = session("ses_newer", appID, "2026-08-13T00:01:00.000Z");
  const foreign = session("ses_foreign", "app_ffffffffffffffffffffffffffffffff", "2026-08-13T00:02:00.000Z");
  let asserted = false;
  const { listCustomerSessions } = await import("../lib/customer-operations.ts");

  const result = await listCustomerSessions(userID, appID, {
    assertApp: async (receivedUserID, receivedAppID) => {
      expect(receivedUserID).toBe(userID);
      expect(receivedAppID).toBe(appID);
      asserted = true;
    },
    snapshot: async (receivedUserID) => {
      expect(receivedUserID).toBe(userID);
      return {
        ready: true,
        observed_at: "2026-08-13T00:03:00.000Z",
        workspaces: [],
        sessions: [older, foreign, newer],
        sandboxes: [],
      };
    },
    terminate: async () => { throw new Error("not used"); },
  });

  expect(asserted).toBe(true);
  expect(result).toEqual({ items: [newer, older] });
});

test("inspects and terminates only a Session already owned by the App", async () => {
  const active = session("ses_active", appID, "2026-08-13T00:01:00.000Z");
  const dependencies = {
    assertApp: async () => undefined,
    snapshot: async () => ({
      ready: true,
      observed_at: "2026-08-13T00:02:00.000Z",
      workspaces: [],
      sessions: [active],
      sandboxes: [],
    }),
    terminate: async (sessionID) => ({ ...active, id: sessionID, state: "idle" }),
  };
  const { getCustomerSession, terminateCustomerSession } = await import("../lib/customer-operations.ts");

  expect(await getCustomerSession(userID, appID, active.id, dependencies)).toEqual(active);
  expect(await terminateCustomerSession(userID, appID, active.id, dependencies)).toEqual({ ...active, state: "idle" });
  await expect(getCustomerSession(userID, appID, "ses_foreign", dependencies)).rejects.toMatchObject({ code: "not_found" });
});
