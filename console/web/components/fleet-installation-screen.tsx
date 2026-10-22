"use client";

import { useSelectedLayoutSegment } from "next/navigation";
import type { ReactNode } from "react";

import { ConsoleShell, type ShellApp, type ShellSection, type ShellWorkspace } from "@/components/console-shell";
import { CopyId } from "@/components/copy-id";
import { InstallationState } from "@/components/fleet-tables";
import { HeadStat, SectionHead } from "@/components/section-head";
import type { FleetInstallation } from "@/lib/customer-fleets";
import { appTone } from "@/lib/shell-data";
import type { Sandbox } from "@/lib/telemetry-types";
import type { Viewer } from "@/lib/viewer";
import { useStreamed } from "@/lib/use-streamed";

export type InstallationSectionId = "overview" | "sandboxes" | "traces";

/** The installation screens' sections, after its overview: what it runs and
 *  what it did, then the rest of its App. `hrefBase` is everything before the
 *  section id — a path prefix for the real routes, a query prefix for the
 *  previews. */
export function installationSections(hrefBase: string, appId: string | null): ShellSection[] {
  return [
    { id: "sandboxes", label: "Sandboxes", href: `${hrefBase}sandboxes`, group: "Monitoring" },
    { id: "traces", label: "Traces", href: `${hrefBase}traces` },
    // Releases, environment, Workspaces and the rest stay on the App's own
    // screens; a deleted installation has none left to open.
    ...(appId ? [{ id: "app", label: "App pages", href: `/apps/${appId}`, group: "App" }] : []),
  ];
}

/** The frame every installation page renders inside: the shell with the
 *  installation's sections in the sidebar and a trail through its Fleet. The
 *  overview carries the header; section pages bring their own. Runtime state
 *  streams in after the first paint, as on an App. */
export function FleetInstallationScreen({
  fleet,
  installation,
  viewer,
  apps,
  workspaces,
  fleets,
  maxApps,
  usedApps,
  runtime: runtimeRead,
  section,
  homeHref: previewHome,
  sectionHrefBase,
  children,
}: {
  fleet: { id: string; name: string };
  installation: FleetInstallation;
  viewer: Viewer;
  apps: ShellApp[];
  workspaces: ShellWorkspace[];
  fleets: { id: string; name: string }[];
  maxApps?: number | null;
  usedApps?: number | null;
  // The account's Sandboxes, or null when runtime state is unavailable.
  runtime: Promise<Sandbox[] | null>;
  // Defaults to the routed segment; previews pass it explicitly.
  section?: InstallationSectionId;
  // Previews swap the routes for their own query links.
  homeHref?: string;
  sectionHrefBase?: string;
  children: ReactNode;
}) {
  const segment = useSelectedLayoutSegment();
  const active: InstallationSectionId = section ?? (segment === "sandboxes" || segment === "traces" ? segment : "overview");
  const fleetHref = `/fleets/${fleet.id}`;
  const homeHref = previewHome ?? `${fleetHref}/installations/${installation.id}`;
  const sections = installationSections(sectionHrefBase ?? `${homeHref}/`, installation.status === "deleted" ? null : installation.app_id);
  const current = sections.find(({ id }) => id === active);

  const sandboxes = useStreamed(runtimeRead);
  const pending = sandboxes === undefined;
  const tone = pending ? "unknown" : appTone(installation.app_id, sandboxes);
  const running = (sandboxes ?? []).filter(
    (sandbox) => sandbox.app_id === installation.app_id && sandbox.state === "running",
  ).length;

  return (
    <ConsoleShell
      viewer={viewer}
      apps={apps}
      workspaces={workspaces}
      fleets={fleets}
      maxApps={maxApps}
      usedApps={usedApps}
      appContext={{
        id: installation.id,
        slug: installation.external_key,
        tone,
        sections,
        activeSection: active === "overview" ? "home" : active,
        homeHref,
        back: { label: fleet.name, href: fleetHref },
      }}
      crumbs={[
        { label: "Home", href: "/profile" },
        { label: "Fleets", href: "/fleets" },
        { label: fleet.name, href: fleetHref },
        ...(active === "overview" || !current
          ? [{ label: installation.external_key }]
          : [{ label: installation.external_key, href: homeHref }, { label: current.label }]),
      ]}
    >
      {active === "overview" && (
        <SectionHead title={installation.external_key} subtitle={<CopyId value={installation.id} />}>
          <HeadStat label="Status" value={<InstallationState status={installation.status} />} />
          <HeadStat label="Package" value={`v${installation.version}`} />
          {(pending || sandboxes !== null) && (
            <HeadStat label="Sandboxes" value={pending ? "—" : running} tone={running === 0 ? "dim" : undefined} />
          )}
        </SectionHead>
      )}
      {children}
    </ConsoleShell>
  );
}
