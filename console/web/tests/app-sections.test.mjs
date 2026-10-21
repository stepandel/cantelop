import { expect, test } from "bun:test";

import { APP_SECTION_IDS, appSections, isAppSectionId } from "../lib/app-sections.ts";

test("the App console exposes sandboxes and traces under monitoring", () => {
  expect(APP_SECTION_IDS).toEqual(["home", "workspaces", "builds", "configuration", "access", "credentials", "api", "sandboxes", "traces"]);
  expect(appSections("/apps/app_test/", { workspaces: 2, builds: 3 })).toEqual([
    { id: "workspaces", label: "Workspaces", href: "/apps/app_test/workspaces", count: "2" },
    { id: "builds", label: "Releases", href: "/apps/app_test/builds", count: "3" },
    { id: "configuration", label: "Environment", href: "/apps/app_test/configuration" },
    { id: "access", label: "Access", href: "/apps/app_test/access" },
    { id: "credentials", label: "Credentials", href: "/apps/app_test/credentials" },
    { id: "api", label: "Ingress", href: "/apps/app_test/api" },
    { id: "sandboxes", label: "Sandboxes", href: "/apps/app_test/sandboxes", group: "Monitoring" },
    { id: "traces", label: "Traces", href: "/apps/app_test/traces" },
  ]);
  for (const section of APP_SECTION_IDS) {
    expect(isAppSectionId(section)).toBe(true);
  }
  for (const removed of ["messages", "requests", "diagnostics"]) {
    expect(isAppSectionId(removed)).toBe(false);
  }
});

test("App and preview navigation render one monitoring heading above both pages", () => {
  for (const base of ["/apps/app_test/", "/preview/app?section="]) {
    const sections = appSections(base, {});
    expect(sections.filter(({ group }) => group === "Monitoring")).toHaveLength(1);
    expect(sections.slice(-2)).toEqual([
      { id: "sandboxes", label: "Sandboxes", href: `${base}sandboxes`, group: "Monitoring" },
      { id: "traces", label: "Traces", href: `${base}traces` },
    ]);
  }
});
