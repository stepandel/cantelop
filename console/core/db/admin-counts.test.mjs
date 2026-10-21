import { expect, test } from "bun:test";

import { getAdminCounts } from "./admin-counts";

test("counts all users and non-archived apps independently across owners", async () => {
  let statement;
  const counts = await getAdminCounts({
    async query(sql) {
      statement = sql;
      return { rows: [{ users: "1234", apps: "5678" }], rowCount: 1 };
    },
  });
  expect(counts).toEqual({ users: 1234, apps: 5678 });
  expect(statement).toContain("(SELECT count(*) FROM cantelop_control.users) AS users");
  expect(statement).toContain("(SELECT count(*) FROM cantelop_control.apps WHERE status <> 'archived') AS apps");
  expect(statement).not.toContain("owner_user_id");
});

test("preserves zero totals for an empty platform", async () => {
  expect(await getAdminCounts({
    async query() {
      return { rows: [{ users: "0", apps: "0" }], rowCount: 1 };
    },
  })).toEqual({ users: 0, apps: 0 });
});

test("propagates database failures instead of reporting zero users and apps", async () => {
  await expect(getAdminCounts({
    async query() { throw new Error("database unavailable"); },
  })).rejects.toThrow("database unavailable");
});
