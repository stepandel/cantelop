import { expect, test } from "bun:test";

import { ADMIN_SECTION_IDS, adminSections, isAdminSectionId } from "../lib/admin-sections.ts";

test("the operator console exposes sandboxes, traces and workspaces alongside telemetry home", () => {
  expect(ADMIN_SECTION_IDS).toEqual(["home", "sandboxes", "traces", "workspaces"]);
  expect(adminSections("/admin/")).toEqual([
    { id: "sandboxes", label: "Sandboxes", href: "/admin/sandboxes" },
    { id: "traces", label: "Traces", href: "/admin/traces" },
    { id: "workspaces", label: "Workspaces", href: "/admin/workspaces" },
  ]);
  for (const section of ADMIN_SECTION_IDS) {
    expect(isAdminSectionId(section)).toBe(true);
  }
  for (const removed of ["sessions", "requests", "messages"]) {
    expect(isAdminSectionId(removed)).toBe(false);
  }
});

test("the preview uses the same admin sections with query navigation", () => {
  expect(adminSections("/preview?section=")).toEqual([
    { id: "sandboxes", label: "Sandboxes", href: "/preview?section=sandboxes" },
    { id: "traces", label: "Traces", href: "/preview?section=traces" },
    { id: "workspaces", label: "Workspaces", href: "/preview?section=workspaces" },
  ]);
});
