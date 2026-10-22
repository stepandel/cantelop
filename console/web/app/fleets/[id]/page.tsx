import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { Suspense } from "react";

import { CopyId } from "@/components/copy-id";
import { FleetCapacity } from "@/components/fleet-capacity";
import { FleetFrame } from "@/components/fleet-frame";
import { FleetPrincipalsPanel } from "@/components/fleet-principals-panel";
import { FleetInstallationsTable, FleetPackagesTable } from "@/components/fleet-tables";
import { PaneLoading } from "@/components/loading-state";
import { HeadStat, SectionHead } from "@/components/section-head";
import { cachedCustomerSnapshot } from "@/lib/app-shell-data";
import {
  FLEET_ID_PATTERN,
  INSTALLATION_ID_PATTERN,
  INSTALLATION_KEY_SEARCH_PATTERN,
  cachedCustomerFleet,
  getFleetInstallations,
  getFleetInstallationsForApps,
  getFleetPackages,
  getFleetPrincipals,
} from "@/lib/customer-fleets";
import { capacityLabel } from "@/lib/fleet-labels";
import { runningByApp } from "@/lib/instances";
import type { Sandbox } from "@/lib/telemetry-types";
import { getUserSession } from "@/lib/user-session";
import { FleetError } from "@core/fleets/model";

export const dynamic = "force-dynamic";
export const metadata = { title: "Fleet" };

// One Fleet: its cap, the installations running right now, every App it has
// installed, the machine principals that act for it and the packages it can
// install. Policy and budget belong to the owning service; the console shows
// them and never sets them.
export default async function FleetPage({ params, searchParams }: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ after?: string | string[]; key?: string | string[] }>;
}) {
  const { id } = await params;
  if (!FLEET_ID_PATTERN.test(id)) notFound();
  const session = await getUserSession();
  if (!session) redirect(`/?callbackUrl=/fleets/${id}`);
  const userID = session.user.id;
  const { after: rawAfter, key: rawKey } = await searchParams;
  const after = typeof rawAfter === "string" && INSTALLATION_ID_PATTERN.test(rawAfter) ? rawAfter : undefined;
  const searched = typeof rawKey === "string" ? rawKey.trim() : "";
  // A search no key could match finds nothing without asking the database.
  const key = searched && INSTALLATION_KEY_SEARCH_PATTERN.test(searched) ? searched : undefined;

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
  // Runtime state is the observer's, the slowest read: it streams into the
  // installation tables after the first paint.
  const runtime: Promise<Sandbox[] | null> = cachedCustomerSnapshot(userID).then((snapshot) => snapshot.sandboxes, () => null);
  // Each section degrades on its own once the Fleet itself is known.
  const [installations, principals, packages] = await Promise.all([
    searched && !key ? { items: [], after: null } : getFleetInstallations(userID, id, after, key).catch((error: unknown) => {
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

      <h2 className="section-label">Running now</h2>
      <div className="section-panel">
        <Suspense fallback={<PaneLoading />}>
          <RunningInstallations userID={userID} fleetId={fleet.id} runtime={runtime} />
        </Suspense>
      </div>

      <h2 className="section-label" id="installations">Installations</h2>
      <div className="section-panel">
        <form className="inline-form compact fleet-search" role="search" method="get" action={`/fleets/${fleet.id}#installations`}>
          <input
            type="search"
            name="key"
            aria-label="External key"
            placeholder="Find by external key prefix"
            defaultValue={searched}
            maxLength={128}
            autoComplete="off"
            spellCheck={false}
          />
          <button type="submit">Find</button>
          {searched && <Link href={`/fleets/${fleet.id}#installations`}>Clear</Link>}
        </form>
        {installations === null ? (
          <div className="notice error">Installations are temporarily unavailable</div>
        ) : (
          <>
            <FleetInstallationsTable
              fleetId={fleet.id}
              installations={installations.items}
              runtime={runtime}
              empty={searched ? <>No installation&rsquo;s external key starts with <code>{searched}</code>.</> : undefined}
            />
            {(after || installations.after) && (
              <p className="table-note fleet-pager">
                {after && <Link href={pageHref(fleet.id, key)}>‹ First page</Link>}
                {installations.after && <Link href={pageHref(fleet.id, key, installations.after)}>Next page ›</Link>}
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

function pageHref(fleetId: string, key: string | undefined, after?: string) {
  const query = new URLSearchParams();
  if (key) query.set("key", key);
  if (after) query.set("after", after);
  const search = query.toString();
  return `/fleets/${fleetId}${search ? `?${search}` : ""}#installations`;
}

// The installations with a Sandbox running, busiest first: where to look
// when the Fleet is doing something. It waits on the observer, so it streams
// in on its own after the rest of the page.
async function RunningInstallations({ userID, fleetId, runtime }: {
  userID: string;
  fleetId: string;
  runtime: Promise<Sandbox[] | null>;
}) {
  const sandboxes = await runtime;
  if (sandboxes === null) return <div className="notice error">Runtime state is temporarily unavailable</div>;
  const running = runningByApp(sandboxes);
  let installations: Awaited<ReturnType<typeof getFleetInstallationsForApps>>;
  try {
    installations = await getFleetInstallationsForApps(userID, fleetId, [...running.keys()]);
  } catch (error) {
    console.error("fleet_running_installations_unavailable", error);
    return <div className="notice error">Installations are temporarily unavailable</div>;
  }
  installations.sort((a, b) => (running.get(b.app_id) ?? 0) - (running.get(a.app_id) ?? 0));
  return (
    <FleetInstallationsTable
      fleetId={fleetId}
      installations={installations}
      runtime={runtime}
      empty="No installation is running a Sandbox right now."
    />
  );
}
