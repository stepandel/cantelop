import type { ShellSection } from "@/components/console-shell";

// The operator console's screens, mirroring the App layout: "home" is /admin
// itself — the host header and its infrastructure graphs — reached through
// the "telemetry" row in the sidebar. Sandboxes and traces provide the detailed
// monitoring surfaces; workspaces lists every App's workspaces platform-wide.
export const ADMIN_SECTION_IDS = [
  "home",
  "sandboxes",
  "traces",
  "workspaces",
] as const;
export type AdminSectionId = (typeof ADMIN_SECTION_IDS)[number];

export function isAdminSectionId(value: string): value is AdminSectionId {
  return (ADMIN_SECTION_IDS as readonly string[]).includes(value);
}

// Builds the shell's section list ("home" is not among them — it is the
// sidebar's telemetry row). `hrefBase` is everything before the section id —
// a path prefix for the real routes, a query prefix for the preview.
export function adminSections(
  hrefBase: string,
): ShellSection[] {
  return [
    { id: "sandboxes", label: "Sandboxes", href: `${hrefBase}sandboxes` },
    { id: "traces", label: "Traces", href: `${hrefBase}traces` },
    { id: "workspaces", label: "Workspaces", href: `${hrefBase}workspaces` },
  ];
}
