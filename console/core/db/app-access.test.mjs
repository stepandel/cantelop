import { expect, test } from "bun:test";

import { ControlError } from "../domain/errors";
import { resolveAppOwnerForUser } from "./app-access";

const ownerID = "usr_0123456789abcdef0123456789abcdef";
const adminID = "usr_fedcba9876543210fedcba9876543210";
const appID = "app_0123456789abcdef0123456789abcdef";

function pool(result) {
  const statements = [];
  return { statements, async query(sql, values) { statements.push({ sql, values }); return result; } };
}

test("resolves the owner's tenant key for an active admin", async () => {
  const database = pool({ rowCount: 1, rows: [{ owner_user_id: ownerID, user_status: "active" }] });
  expect(await resolveAppOwnerForUser(database, adminID, appID)).toBe(ownerID);
  expect(database.statements[0].values).toEqual([appID, adminID]);
  expect(database.statements[0].sql).toContain("c.role = 'admin'");
});

test("hides Apps the actor neither owns nor administers", async () => {
  await expect(resolveAppOwnerForUser(pool({ rowCount: 0, rows: [] }), adminID, appID)).rejects.toEqual(new ControlError("not_found"));
});

test("refuses a suspended actor even when they are a member", async () => {
  const database = pool({ rowCount: 1, rows: [{ owner_user_id: ownerID, user_status: "suspended" }] });
  await expect(resolveAppOwnerForUser(database, adminID, appID)).rejects.toEqual(new ControlError("user_suspended"));
});
