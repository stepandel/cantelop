import { notFound, redirect } from "next/navigation";

import { CopyId } from "@/components/copy-id";
import { FleetFrame } from "@/components/fleet-frame";
import { FleetInstallationDetail } from "@/components/fleet-installation-detail";
import { InstallationState } from "@/components/fleet-tables";
import { HeadStat, SectionHead } from "@/components/section-head";
import {
  FLEET_ID_PATTERN,
  INSTALLATION_ID_PATTERN,
  cachedCustomerFleet,
  getFleetInstallation,
} from "@/lib/customer-fleets";
import { getUserSession } from "@/lib/user-session";
import { FleetError } from "@core/fleets/model";

export const dynamic = "force-dynamic";
export const metadata = { title: "Installation" };

export default async function FleetInstallationPage({ params }: {
  params: Promise<{ id: string; installation: string }>;
}) {
  const { id, installation: installationID } = await params;
  if (!FLEET_ID_PATTERN.test(id) || !INSTALLATION_ID_PATTERN.test(installationID)) notFound();
  const session = await getUserSession();
  if (!session) redirect(`/?callbackUrl=/fleets/${id}/installations/${installationID}`);
  const userID = session.user.id;

  let read: [Awaited<ReturnType<typeof cachedCustomerFleet>>, Awaited<ReturnType<typeof getFleetInstallation>>];
  try {
    read = await Promise.all([cachedCustomerFleet(userID, id), getFleetInstallation(userID, id, installationID)]);
  } catch (error) {
    if (error instanceof FleetError && error.code === "not_found") notFound();
    console.error("fleet_installation_unavailable", error);
    return (
      <FleetFrame
        session={session}
        crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets", href: "/fleets" }, { label: installationID }]}
      >
        <div className="notice error">Installation records are temporarily unavailable</div>
      </FleetFrame>
    );
  }
  const [fleet, installation] = read;

  return (
    <FleetFrame
      session={session}
      crumbs={[
        { label: "Home", href: "/profile" },
        { label: "Fleets", href: "/fleets" },
        { label: fleet.name, href: `/fleets/${fleet.id}` },
        { label: installation.external_key },
      ]}
    >
      <SectionHead title={installation.external_key} subtitle={<CopyId value={installation.id} />}>
        <HeadStat label="Status" value={<InstallationState status={installation.status} />} />
        <HeadStat label="Package" value={`v${installation.version}`} />
      </SectionHead>
      <FleetInstallationDetail installation={installation} />
    </FleetFrame>
  );
}
