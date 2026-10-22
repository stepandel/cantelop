import { requireInstallation } from "@/app/fleets/[id]/installations/[installation]/section-data";
import { FleetInstallationDetail } from "@/components/fleet-installation-detail";

export const dynamic = "force-dynamic";
export const metadata = { title: "Installation" };

// The installation's overview: the screen carries its header, and the body is
// its lifecycle, the policy its service set and its budget epochs.
export default async function FleetInstallationPage({ params }: {
  params: Promise<{ id: string; installation: string }>;
}) {
  const { id, installation: installationID } = await params;
  const { installation } = await requireInstallation(id, installationID);
  // The layout shows the failure in place of this page.
  if (!installation) return null;
  return <FleetInstallationDetail installation={installation} />;
}
