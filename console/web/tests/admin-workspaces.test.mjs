import { expect, mock, test } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

mock.module("next/navigation", () => ({ useRouter: () => ({ refresh() {} }) }));
const { AdminWorkspacesSection, DatabaseDriftPanel } = await import("../components/admin-workspaces-section.tsx");

const workspaces = [
  { id: "wsp_a", slug: "production", app_id: "app_1", app_slug: "acme", status: "active", database: "ready", database_name: "ct-staging-a", created_at: "2026-09-01T10:00:00Z", archived_at: null },
  { id: "wsp_b", slug: "staging", app_id: "app_1", app_slug: "acme", status: "deleting", database: "deleting", database_name: "ct-staging-b", created_at: "2026-09-02T10:00:00Z", archived_at: "2026-09-03T10:00:00Z" },
  { id: "wsp_c", slug: "failed", app_id: "app_1", app_slug: "acme", status: "deletion-failed", database: "ready", database_name: "ct-staging-c", created_at: "2026-09-03T10:00:00Z", archived_at: "2026-09-04T10:00:00Z" },
];

test("summarizes active and unfinished Workspace deletions without an archived count", () => {
  const html = renderToStaticMarkup(createElement(AdminWorkspacesSection, { workspaces }));

  expect(html).toContain("<span>Active</span><strong>1</strong>");
  expect(html).toContain("<span>Deleting</span><strong>1</strong>");
  expect(html).toContain("<span>Deletion failed</span><strong>1</strong>");
  expect(html).not.toContain("Not purged");
  expect(html).toContain("Deletion requested");
  expect(html).not.toContain("Archived");
  expect(html).toContain("Deletion failed</span>");
});

test("shows each Workspace's database and only the lifecycle action that applies", () => {
  const html = renderToStaticMarkup(createElement(AdminWorkspacesSection, { workspaces: [
    ...workspaces,
    { id: "wsp_d", slug: "legacy", app_id: "app_2", app_slug: "gone", status: "unpurged", database: "ready", database_name: "ct-staging-d", created_at: "2026-09-04T10:00:00Z", archived_at: "2026-09-05T10:00:00Z" },
    { id: "wsp_e", slug: "new", app_id: "app_1", app_slug: "acme", status: "active", database: "failed", database_name: null, created_at: "2026-09-05T10:00:00Z", archived_at: null },
  ] }));

  expect(html).toContain("<th>Database</th>");
  expect(html).toContain('title="ct-staging-a"');
  expect(html).toContain("Provisioning failed");
  expect(html).toContain("Name not yet reserved");
  expect(html).toContain("<span>Not purged</span><strong>1</strong>");
  expect(html).toContain('aria-label="Delete acme/production"');
  expect(html).toContain('aria-label="Delete acme/new"');
  expect(html).not.toContain('aria-label="Delete acme/staging"');
  expect(html).not.toContain("Retry deletion of acme/staging");
  expect(html).toContain('aria-label="Retry deletion of acme/failed"');
  expect(html).toContain('aria-label="Retry deletion of gone/legacy"');
  expect(html).toContain(">Purge</button>");
  expect(html).toContain("Check database drift");
});

test("drift panel reports a clean comparison and lists missing and orphaned databases", () => {
  const clean = renderToStaticMarkup(createElement(DatabaseDriftPanel, { initial: {
    checked_at: "2026-10-05T12:00:00Z", provider_databases: 4, missing: [], orphaned: [],
  } }));
  expect(clean).toContain("4 Turso databases match the control database");

  const html = renderToStaticMarkup(createElement(DatabaseDriftPanel, { initial: {
    checked_at: "2026-10-05T12:00:00Z", provider_databases: 2,
    missing: [{ workspace_id: "wsp_a", workspace_slug: "production", app_slug: "acme", database_name: "ct-staging-a" }],
    orphaned: [{ database_name: "ct-staging-stray", workspace_id: null, workspace_slug: null, app_slug: null }],
  } }));
  expect(html).toContain("Missing");
  expect(html).toContain("acme/production");
  expect(html).toContain("Orphaned");
  expect(html).toContain("ct-staging-stray");
  expect(html).toContain("Check database drift again");
});
