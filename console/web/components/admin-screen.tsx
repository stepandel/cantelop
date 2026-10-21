"use client";

import { useSelectedLayoutSegment } from "next/navigation";
import type { ReactNode } from "react";

import { ConsoleShell } from "@/components/console-shell";
import { adminSections, isAdminSectionId, type AdminSectionId } from "@/lib/admin-sections";
import type { Snapshot } from "@/lib/telemetry-types";
import type { Viewer } from "@/lib/viewer";

/** The frame every operator screen renders inside: the shell with the host's
 *  section nav and the section body as children — the operator counterpart
 *  of AppScreen. The Next layout at /admin owns the real instance; the
 *  preview mounts it directly with fixtures. */
export function AdminScreen({
  viewer,
  snapshot,
  section,
  sectionHrefBase,
  children,
}: {
  viewer: Viewer;
  // The host snapshot as of the page load; null degrades the sidebar counts
  // and dots the telemetry row unknown.
  snapshot: Snapshot | null;
  // The active section. Defaults to the routed segment under /admin; the
  // preview, which renders elsewhere, passes it explicitly.
  section?: AdminSectionId;
  // Everything before the section id in a section link; the preview swaps
  // the path prefix for its own query prefix.
  sectionHrefBase?: string;
  children: ReactNode;
}) {
  const segment = useSelectedLayoutSegment();
  // No segment means /admin itself — the host's home.
  const activeSection: AdminSectionId =
    section ?? (segment !== null && isAdminSectionId(segment) ? segment : "home");

  const tone = snapshot === null ? "unknown" : snapshot.ready ? "running" : "degraded";

  const sections = adminSections(sectionHrefBase ?? "/admin/");
  const homeHref = sectionHrefBase === undefined ? "/admin" : `${sectionHrefBase}home`;

  return (
    <ConsoleShell
      viewer={viewer}
      operator
      appContext={{ id: "host", slug: "telemetry", tone, sections, activeSection, homeHref }}
      crumbs={[
        { label: "Host" },
        ...(activeSection === "home"
          ? [{ label: "Telemetry" }]
          : [{ label: "Telemetry", href: homeHref }, { label: sections.find(({ id }) => id === activeSection)?.label ?? activeSection }]),
      ]}
    >
      {children}
    </ConsoleShell>
  );
}
