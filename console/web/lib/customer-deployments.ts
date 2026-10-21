import "server-only";

import { deploymentControl } from "@core/deployment-uploads/control";
import type { Release } from "@/lib/release-status";

// Releases are owned rows, so the repository filters by owner; a caller that
// passes another user's App gets an empty list rather than a leak.
export async function getCustomerReleases(userID: string, appID: string): Promise<Release[]> {
  return (await deploymentControl().list(userID, appID)).items;
}

// One query for every App the user may operate, instead of a history read per App.
export async function getCustomerActiveReleases(userID: string): Promise<Record<string, { version: number; activated_at?: string }>> {
  return deploymentControl().activeReleases(userID);
}
