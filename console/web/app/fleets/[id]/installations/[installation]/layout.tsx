import type { ReactNode } from "react";

import { requireInstallation } from "@/app/fleets/[id]/installations/[installation]/section-data";
import { FleetFrame, loadFleetShell } from "@/components/fleet-frame";
import { FleetInstallationScreen } from "@/components/fleet-installation-screen";
import { cachedCustomerSnapshot } from "@/lib/app-shell-data";
import { viewerOf } from "@/lib/viewer";

export const metadata = { title: "Installation" };

// The frame around every installation page: one place resolves the Fleet and
// the installation, and the sidebar lists what can be inspected in it. The
// frame waits on control-database reads only; runtime state comes from the
// observer and streams in after the first paint.
export default async function FleetInstallationLayout({ params, children }: {
  params: Promise<{ id: string; installation: string }>;
  children: ReactNode;
}) {
  const { id, installation: installationID } = await params;
  const { session, fleet, installation } = await requireInstallation(id, installationID);
  if (!fleet || !installation) {
    return (
      <FleetFrame
        session={session}
        crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets", href: "/fleets" }, { label: installationID }]}
      >
        <div className="notice error">Installation records are temporarily unavailable</div>
      </FleetFrame>
    );
  }
  // The section pages share this request-memoized read.
  const runtime = cachedCustomerSnapshot(session.user.id).then((snapshot) => snapshot.sandboxes, () => null);
  const shell = await loadFleetShell(session.user.id);
  return (
    <FleetInstallationScreen
      viewer={viewerOf(session)}
      {...shell}
      fleet={{ id: fleet.id, name: fleet.name }}
      installation={installation}
      runtime={runtime}
    >
      {children}
    </FleetInstallationScreen>
  );
}
