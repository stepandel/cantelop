import { redirect } from "next/navigation";

import { ProfileView, type AccountRuntime, type AppDeployment } from "@/components/profile-view";
import { getCustomerActiveReleases } from "@/lib/customer-deployments";
import { cachedCustomerWorkspaces, getCustomerApps, getCustomerQuota, getCustomerSnapshot } from "@/lib/customer-telemetry";
import { getUserSession } from "@/lib/user-session";
import { viewerOf } from "@/lib/viewer";

export const dynamic = "force-dynamic";

export const metadata = { title: "Home" };

export default async function ProfilePage() {
  const session = await getUserSession();
  if (!session) redirect("/?callbackUrl=/profile");
  const userID = session.user.id;

  // The listing, the live snapshot and the serving builds are independent
  // reads. Only the listing is essential; the others degrade on their own.
  // The snapshot comes from the observer, the slowest of them, so the page
  // streams it in after the first paint instead of waiting on it.
  const runtimeRead: Promise<AccountRuntime> = getCustomerSnapshot(userID).then(
    (snapshot) => ({ instances: snapshot.sandboxes, observedAt: snapshot.observed_at }),
    (error: unknown) => {
      console.error("profile_snapshot_unavailable", error);
      return null;
    },
  );
  const [listing, releasesRead] = await Promise.allSettled([
    Promise.all([getCustomerApps(userID), cachedCustomerWorkspaces(userID), getCustomerQuota(userID)]),
    getCustomerActiveReleases(userID),
  ]);

  if (listing.status === "rejected") {
    console.error("profile_listing_unavailable", listing.reason);
    return (
      <ProfileView
        viewer={viewerOf(session)}
        initialApps={[]}
        maxApps={null}
        usedApps={null}
        workspaces={[]}
        runtime={Promise.resolve(null)}
        deployments={{}}
        unavailable
      />
    );
  }
  const [{ items: apps }, { items: workspaces }, quota] = listing.value;

  // Each card names the build serving its app and when it went live. When the
  // releases cannot be read the cards have no entry and say nothing.
  const deployments: Record<string, AppDeployment | null> = {};
  if (releasesRead.status === "fulfilled") {
    for (const app of apps) deployments[app.id] = releasesRead.value[app.id] ?? null;
  } else {
    console.error("profile_releases_unavailable", releasesRead.reason);
  }

  return (
    <ProfileView
      viewer={viewerOf(session)}
      initialApps={apps}
      maxApps={quota.maxApps ?? null}
      usedApps={quota.usedApps}
      workspaces={workspaces}
      runtime={runtimeRead}
      deployments={deployments}
      unavailable={false}
    />
  );
}
