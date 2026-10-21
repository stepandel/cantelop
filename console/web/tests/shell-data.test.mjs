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
