import { expect, test } from "bun:test";

import { buildShellApps } from "../lib/shell-data.ts";

const app = { id: "app_one", slug: "one", role: "owner" };
const activeWorkspace = { id: "wsp_one", app_id: app.id };

function instance(id, state) {
  return { id, app_id: app.id, state };
}

test("reports unknown when runtime telemetry is unavailable", () => {
  expect(buildShellApps([app], [activeWorkspace], null)).toEqual([
    { id: app.id, slug: app.slug, tone: "unknown", count: undefined, role: "owner" },
  ]);
});

test("current live Sandboxes are counted separately from terminated Sandboxes", () => {
  const result = buildShellApps(
    [app],
    [activeWorkspace],
		[instance("sbx_previous", "terminated"), instance("sbx_current", "running")],
  );

  expect(result[0]).toEqual({ id: app.id, slug: app.slug, tone: "running", count: "1", role: "owner" });
});

test("reports idle when only terminated Sandboxes remain", () => {
	expect(buildShellApps([app], [activeWorkspace], [instance("sbx_previous", "terminated")])[0]?.tone).toBe("idle");
});

test("leaves Fleet installations and their Workspaces out of the palette", async () => {
  const { buildShellWorkspaces } = await import("../lib/shell-data.ts");
  const installation = { id: "app_installed", slug: "j-1", role: "owner", fleet_id: "flt_one" };
  expect(buildShellApps([app, installation], [], null).map(({ id }) => id)).toEqual([app.id]);
  expect(buildShellWorkspaces([app, installation], [
    { id: "wsp_one", app_id: app.id, slug: "prod" },
    { id: "wsp_installed", app_id: installation.id, slug: "prod" },
  ]).map(({ id }) => id)).toEqual(["wsp_one"]);
});
