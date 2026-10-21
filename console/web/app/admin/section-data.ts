import { cache } from "react";
import { redirect } from "next/navigation";
import { getAdminCounts } from "@core/db/admin-counts";

import { getAdminSession } from "@/lib/admin-session";
import { getSnapshot } from "@/lib/fire-fuse";

// Per-request memoized host snapshot: the admin layout and the section page
// it wraps both need it on a full load, and cache() collapses the double
// read into one observer call.
const cachedHostSnapshot = cache(getSnapshot);

// The guard every operator page opens with. The layout runs the same check
// for the frame; the page still owns its own access to the data it shows.
// No section means /admin itself — the host's home.
export async function requireAdminSession(section?: string) {
  const session = await getAdminSession();
  if (!session) redirect(`/?callbackUrl=/admin${section ? `/${section}` : ""}`);
  return session;
}

export async function loadHostSnapshot() {
  try {
    return { snapshot: await cachedHostSnapshot(), unavailable: false };
  } catch {
    return { snapshot: null, unavailable: true };
  }
}

export async function loadAdminCounts() {
  try {
    return await getAdminCounts();
  } catch {
    // A database outage must not hide the independently loaded host telemetry.
    return null;
  }
}
