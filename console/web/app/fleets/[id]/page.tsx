import Link from "next/link";
import { notFound, redirect } from "next/navigation";

import { CopyId } from "@/components/copy-id";
import { FleetCapacity } from "@/components/fleet-capacity";
import { FleetFrame } from "@/components/fleet-frame";
import { FleetPrincipalsPanel } from "@/components/fleet-principals-panel";
import { FleetInstallationsTable, FleetPackagesTable } from "@/components/fleet-tables";
import { HeadStat, SectionHead } from "@/components/section-head";
import {
  FLEET_ID_PATTERN,
  INSTALLATION_ID_PATTERN,
  cachedCustomerFleet,
  getFleetInstallations,
  getFleetPackages,
  getFleetPrincipals,
} from "@/lib/customer-fleets";
import { capacityLabel } from "@/lib/fleet-labels";
import { getUserSession } from "@/lib/user-session";
import { FleetError } from "@core/fleets/model";

export const dynamic = "force-dynamic";
export const metadata = { title: "Fleet" };

// One Fleet: its cap, the Apps it has installed, the machine principals that
// act for it and the packages it can install. Policy and budget belong to the
// owning service; the console shows them and never sets them.
export default async function FleetPage({ params, searchParams }: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ after?: string | string[] }>;
}) {
  const { id } = await params;
  if (!FLEET_ID_PATTERN.test(id)) notFound();
  const session = await getUserSession();
  if (!session) redirect(`/?callbackUrl=/fleets/${id}`);
  const userID = session.user.id;
  const rawAfter = (await searchParams).after;
  const after = typeof rawAfter === "string" && INSTALLATION_ID_PATTERN.test(rawAfter) ? rawAfter : undefined;

  let fleet: Awaited<ReturnType<typeof cachedCustomerFleet>>;
  try {
    fleet = await cachedCustomerFleet(userID, id);
  } catch (error) {
    if (error instanceof FleetError && error.code === "not_found") notFound();
    console.error("fleet_unavailable", error);
    return (
      <FleetFrame session={session} crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets", href: "/fleets" }, { label: id }]}>
        <div className="notice error">Fleet records are temporarily unavailable</div>
      </FleetFrame>
    );
  }
  // Each section degrades on its own once the Fleet itself is known.
  const [installations, principals, packages] = await Promise.all([
    getFleetInstallations(userID, id, after).catch((error: unknown) => {
      console.error("fleet_installations_unavailable", error);
      return null;
    }),
    getFleetPrincipals(userID, id).catch((error: unknown) => {
      console.error("fleet_principals_unavailable", error);
      return null;
    }),
    getFleetPackages(userID, id).catch((error: unknown) => {
      console.error("fleet_packages_unavailable", error);
      return null;
    }),
  ]);
  const full = fleet.max_apps !== null && fleet.used_apps >= fleet.max_apps;

  return (
    <FleetFrame
      session={session}
      crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets", href: "/fleets" }, { label: fleet.name }]}
    >
      <SectionHead title={fleet.name} subtitle={<CopyId value={fleet.id} />}>
        <HeadStat label="Installations" value={capacityLabel(fleet.used_apps, fleet.max_apps)} tone={full ? "warn" : undefined} />
        <HeadStat label="Principals" value={fleet.active_principals} tone={fleet.active_principals === 0 ? "dim" : undefined} />
      </SectionHead>

      <div className="section-panel">
        <FleetCapacity fleetId={fleet.id} maxApps={fleet.max_apps} usedApps={fleet.used_apps} createdAt={fleet.created_at} />
      </div>

      <h2 className="section-label">Installations</h2>
      <div className="section-panel">
        {installations === null ? (
          <div className="notice error">Installations are temporarily unavailable</div>
        ) : (
          <>
            <FleetInstallationsTable fleetId={fleet.id} installations={installations.items} />
            {(after || installations.after) && (
              <p className="table-note fleet-pager">
                {after && <Link href={`/fleets/${fleet.id}`}>‹ First page</Link>}
                {installations.after && <Link href={`/fleets/${fleet.id}?after=${installations.after}`}>Next page ›</Link>}
              </p>
            )}
          </>
        )}
      </div>

      <h2 className="section-label">Principals</h2>
      <div className="section-panel">
        {principals === null
          ? <div className="notice error">Principals are temporarily unavailable</div>
          : <FleetPrincipalsPanel fleetId={fleet.id} initialPrincipals={principals} />}
      </div>

      <h2 className="section-label">Packages</h2>
      <div className="section-panel">
        {packages === null
          ? <div className="notice error">Packages are temporarily unavailable</div>
          : <FleetPackagesTable packages={packages} />}
      </div>
    </FleetFrame>
  );
}
