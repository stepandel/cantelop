import type { ShellSection } from "@/components/console-shell";

// The App's screens. "home" is /apps/[id] itself — the App's header and its
// runtime graphs — reached through the App's name in the sidebar. The rest
// are pages at /apps/[id]/<section>, in sidebar order: what an App *is*
// (workspaces, releases, configuration, who and what may reach it, its
// ingress) first, then sandboxes and traces under the monitoring heading.
export const APP_SECTION_IDS = [
  "home",
  "workspaces",
  "builds",
  "configuration",
  "access",
  "credentials",
  "api",
  "sandboxes",
  "traces",
] as const;
export type AppSectionId = (typeof APP_SECTION_IDS)[number];

export function isAppSectionId(value: string): value is AppSectionId {
  return (APP_SECTION_IDS as readonly string[]).includes(value);
}

// Builds the shell's section list ("home" is not among them — it is the
// sidebar's app row). `hrefBase` is everything before the section id — a path
// prefix for the real routes, a query prefix for the previews.
export function appSections(
  hrefBase: string,
  counts: { workspaces?: number; builds?: number },
): ShellSection[] {
  const count = (value: number | undefined) => (value === undefined ? undefined : String(value));
  return [
    { id: "workspaces", label: "Workspaces", href: `${hrefBase}workspaces`, count: count(counts.workspaces) },
    { id: "builds", label: "Releases", href: `${hrefBase}builds`, count: count(counts.builds) },
    { id: "configuration", label: "Environment", href: `${hrefBase}configuration` },
    { id: "access", label: "Access", href: `${hrefBase}access` },
    { id: "credentials", label: "Credentials", href: `${hrefBase}credentials` },
    { id: "api", label: "Ingress", href: `${hrefBase}api` },
    { id: "sandboxes", label: "Sandboxes", href: `${hrefBase}sandboxes`, group: "Monitoring" },
    { id: "traces", label: "Traces", href: `${hrefBase}traces` },
  ];
}
