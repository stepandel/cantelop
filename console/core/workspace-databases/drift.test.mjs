import { expect, test } from "bun:test";

import { checkWorkspaceDatabaseDrift } from "./drift";

const rows = [
  { workspace_id: "wsp_ready", workspace_slug: "prod", app_slug: "acme", status: "ready", provider_name: "ct-staging-ready" },
  { workspace_id: "wsp_gone", workspace_slug: "lost", app_slug: "acme", status: "ready", provider_name: "ct-staging-gone" },
  { workspace_id: "wsp_pending", workspace_slug: "new", app_slug: "acme", status: "pending", provider_name: "ct-staging-pending" },
  { workspace_id: "wsp_deleting", workspace_slug: "old", app_slug: "acme", status: "deleting", provider_name: "ct-staging-deleting" },
  { workspace_id: "wsp_tomb", workspace_slug: "tomb", app_slug: "acme", status: "deleted", provider_name: "ct-staging-tomb" },
];

test("reports ready records missing from Turso and unreachable Turso databases", async () => {
  const order = [];
  let parameters;
  const drift = await checkWorkspaceDatabaseDrift(
    { async query(_sql, values) { order.push("records"); parameters = values; return { rows, rowCount: rows.length }; } },
    { async list() { order.push("provider"); return ["ct-staging-ready", "ct-staging-pending", "ct-staging-deleting", "ct-staging-tomb", "ct-staging-stray", "ct-preview-other", "unrelated"]; } },
    "staging",
    () => new Date("2026-10-05T12:00:00Z"),
  );
  expect(order).toEqual(["provider", "records"]);
  expect(parameters).toEqual(["ct-staging-%"]);
  expect(drift).toEqual({
    checked_at: "2026-10-05T12:00:00.000Z",
    provider_databases: 5,
    missing: [{ workspace_id: "wsp_gone", workspace_slug: "lost", app_slug: "acme", database_name: "ct-staging-gone" }],
    orphaned: [
      { database_name: "ct-staging-stray", workspace_id: null, workspace_slug: null, app_slug: null },
      { database_name: "ct-staging-tomb", workspace_id: "wsp_tomb", workspace_slug: "tomb", app_slug: "acme" },
    ],
  });
});

test("propagates provider failures instead of reporting every record missing", async () => {
  await expect(checkWorkspaceDatabaseDrift(
    { async query() { return { rows, rowCount: rows.length }; } },
    { async list() { throw new Error("provider unavailable"); } },
    "staging",
  )).rejects.toThrow("provider unavailable");
});
