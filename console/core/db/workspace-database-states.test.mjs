import { expect, test } from "bun:test";

import { listWorkspaceDatabaseStates, workspaceDatabaseState } from "./workspace-database-states";

test("derives a failed state only when the latest registration stopped retrying", () => {
  expect(workspaceDatabaseState("pending", null)).toBe("provisioning");
  expect(workspaceDatabaseState("pending", "succeeded")).toBe("on-demand");
  expect(workspaceDatabaseState("pending", "queued")).toBe("provisioning");
  expect(workspaceDatabaseState("pending", "failed")).toBe("failed");
  expect(workspaceDatabaseState("ready", "failed")).toBe("ready");
  expect(workspaceDatabaseState("deleting", null)).toBe("deleting");
  expect(workspaceDatabaseState(null, null)).toBeNull();
});

test("lists live Workspace database states for an accessible App without provider details", async () => {
  let statement; let parameters;
  const states = await listWorkspaceDatabaseStates("usr_1", "app_1", {
    async query(sql, values) {
      statement = sql; parameters = values;
      return { rows: [
        { id: "wsp_a", status: "ready", registration_status: "succeeded" },
        { id: "wsp_b", status: "pending", registration_status: "failed" },
        { id: "wsp_c", status: null, registration_status: null },
      ], rowCount: 3 };
    },
  });
  expect(states).toEqual({ wsp_a: "ready", wsp_b: "failed" });
  expect(parameters).toEqual(["app_1", "usr_1"]);
  expect(statement).toContain("w.archived_at IS NULL AND a.status <> 'archived'");
  expect(statement).toContain("a.owner_user_id = $2");
  expect(statement).not.toContain("provider_name");
  expect(statement).not.toContain("token");
});
