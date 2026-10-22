import { notFound, redirect } from "next/navigation";

import { FLEET_ID_PATTERN, INSTALLATION_ID_PATTERN, cachedCustomerFleet, cachedFleetInstallation } from "@/lib/customer-fleets";
import { getUserSession } from "@/lib/user-session";
import { FleetError } from "@core/fleets/model";

// The guard the installation layout and every section page open with. The
// reads are request-memoized, so a page shares the layout's. An unknown or
// foreign installation is a 404; any other failure is null, and the layout
// shows it as unavailable.
export async function requireInstallation(id: string, installationID: string, section?: string) {
  if (!FLEET_ID_PATTERN.test(id) || !INSTALLATION_ID_PATTERN.test(installationID)) notFound();
  const session = await getUserSession();
  if (!session) redirect(`/?callbackUrl=/fleets/${id}/installations/${installationID}${section ? `/${section}` : ""}`);
  const userID = session.user.id;
  try {
    const [fleet, installation] = await Promise.all([
      cachedCustomerFleet(userID, id),
      cachedFleetInstallation(userID, id, installationID),
    ]);
    return { session, fleet, installation };
  } catch (error) {
    if (error instanceof FleetError && error.code === "not_found") notFound();
    console.error("fleet_installation_unavailable", error);
    return { session, fleet: null, installation: null };
  }
}
