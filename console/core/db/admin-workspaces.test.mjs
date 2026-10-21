import { expect, test } from "bun:test";

import { listAdminWorkspaces } from "./admin-workspaces";

test("lists live workspaces and unfinished deletions while hiding purged tombstones", async () => {
  let statement;
  const workspaces = await listAdminWorkspaces({
    async query(sql) {
      statement = sql;
      return {
        rows: [
          { id: "wsp_e", slug: "legacy", app_id: "app_2", app_slug: "gone", created_at: new Date("2026-09-05T10:00:00Z"), archived_at: new Date("2026-09-06T10:00:00Z"), purge_status: null, database_status: "ready", database_name: "ct-staging-e", registration_status: "succeeded" },
          { id: "wsp_d", slug: "purged", app_id: "app_1", app_slug: "acme", created_at: new Date("2026-09-04T10:00:00Z"), archived_at: new Date("2026-09-05T10:00:00Z"), purge_status: "succeeded", database_status: "deleted", database_name: "ct-staging-d", registration_status: "succeeded" },
          { id: "wsp_c", slug: "failed", app_id: "app_1", app_slug: "acme", created_at: new Date("2026-09-03T10:00:00Z"), archived_at: new Date("2026-09-04T10:00:00Z"), purge_status: "failed", database_status: "deleting", database_name: "ct-staging-c", registration_status: "succeeded" },
          { id: "wsp_b", slug: "staging", app_id: "app_1", app_slug: "acme", created_at: new Date("2026-09-02T10:00:00Z"), archived_at: new Date("2026-09-03T10:00:00Z"), purge_status: "running", database_status: "pending", database_name: null, registration_status: "failed" },
          { id: "wsp_a", slug: "production", app_id: "app_1", app_slug: "acme", created_at: "2026-09-01T10:00:00Z", archived_at: null, purge_status: null, database_status: "pending", database_name: "ct-staging-a", registration_status: "running" },
        ],
        rowCount: 5,
      };
    },
  });
  expect(workspaces).toEqual([
    { id: "wsp_e", slug: "legacy", app_id: "app_2", app_slug: "gone", status: "unpurged", database: "ready", database_name: "ct-staging-e", created_at: "2026-09-05T10:00:00.000Z", archived_at: "2026-09-06T10:00:00.000Z" },
    { id: "wsp_c", slug: "failed", app_id: "app_1", app_slug: "acme", status: "deletion-failed", database: "deleting", database_name: "ct-staging-c", created_at: "2026-09-03T10:00:00.000Z", archived_at: "2026-09-04T10:00:00.000Z" },
    { id: "wsp_b", slug: "staging", app_id: "app_1", app_slug: "acme", status: "deleting", database: "failed", database_name: null, created_at: "2026-09-02T10:00:00.000Z", archived_at: "2026-09-03T10:00:00.000Z" },
    { id: "wsp_a", slug: "production", app_id: "app_1", app_slug: "acme", status: "active", database: "provisioning", database_name: "ct-staging-a", created_at: "2026-09-01T10:00:00.000Z", archived_at: null },
  ]);
  expect(statement).toContain("JOIN cantelop_control.apps a ON a.id = w.app_id");
  expect(statement).toContain("purge.kind = 'purge_workspace'");
  expect(statement).toContain("purge.status IS DISTINCT FROM 'succeeded'");
  expect(statement).toContain("ORDER BY w.created_at DESC");
  expect(statement).toContain("LEFT JOIN cantelop_control.workspace_databases d");
  expect(statement).toContain("registration.input->>'workspace_id' = w.id");
});

test("propagates database failures instead of reporting an empty platform", async () => {
  await expect(listAdminWorkspaces({
    async query() { throw new Error("database unavailable"); },
  })).rejects.toThrow("database unavailable");
});
