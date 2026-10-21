import type { WorkspaceDatabaseState } from "@core/db/workspace-database-states";

export const databaseLabels: Record<WorkspaceDatabaseState, string> = {
  "on-demand": "On first use",
  provisioning: "Provisioning",
  ready: "Ready",
  failed: "Provisioning failed",
  deleting: "Deleting",
  deleted: "Deleted",
};
