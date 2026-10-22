import { redirect } from "next/navigation";

import { FleetFrame } from "@/components/fleet-frame";
import { FleetsIndex } from "@/components/fleets-index";
import { HeadStat, SectionHead } from "@/components/section-head";
import { cachedCustomerQuota } from "@/lib/app-shell-data";
import { cachedCustomerFleets, type CustomerFleet } from "@/lib/customer-fleets";
import { getUserSession } from "@/lib/user-session";

export const dynamic = "force-dynamic";
export const metadata = { title: "Fleets" };

export default async function FleetsPage() {
  const session = await getUserSession();
  if (!session) redirect("/?callbackUrl=/fleets");
  const userID = session.user.id;

  let fleets: CustomerFleet[] | null = null;
  try {
    fleets = await cachedCustomerFleets(userID);
  } catch (error) {
    console.error("fleets_unavailable", error);
  }
  const quota = await cachedCustomerQuota(userID).catch(() => null);
  const atQuota = quota !== null && quota.maxApps !== null && quota.usedApps >= quota.maxApps;

  return (
    <FleetFrame session={session} crumbs={[{ label: "Home", href: "/profile" }, { label: "Fleets" }]}>
      <SectionHead title="Fleets" subtitle="Isolated Apps your service installs for its own customers">
        <HeadStat label="Fleets" value={fleets?.length ?? "—"} />
        <HeadStat
          label="Installations"
          value={fleets ? fleets.reduce((total, fleet) => total + fleet.used_apps, 0) : "—"}
        />
      </SectionHead>
      {fleets === null
        ? <div className="notice error">Fleet records are temporarily unavailable</div>
        : <FleetsIndex fleets={fleets} atQuota={atQuota} />}
    </FleetFrame>
  );
}
