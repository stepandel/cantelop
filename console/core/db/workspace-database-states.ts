import { appAccessibleSQL } from "./app-access";
import { controlDatabase, type SQLClient } from "./database";

export type WorkspaceDatabaseState = "provisioning" | "on-demand" | "ready" | "failed" | "deleting" | "deleted";

// The latest register_workspace operation decides whether a pending database
// is still provisioning or has stopped retrying. Existing Workspaces were
// queued by migration 0019 under a different idempotency key, so match on input.
export const latestRegistrationSQL = `LEFT JOIN LATERAL (
  SELECT registration.status FROM cantelop_control.operations registration
  WHERE registration.app_id = w.app_id AND registration.kind = 'register_workspace'
    AND registration.input->>'workspace_id' = w.id
  ORDER BY registration.created_at DESC LIMIT 1
) registration ON true`;

export function workspaceDatabaseState(status: string | null, registrationStatus: string | null): WorkspaceDatabaseState | null {
  switch (status) {
    case "ready": case "deleting": case "deleted": return status;
    case "pending": return registrationStatus === "failed" ? "failed" : registrationStatus === "succeeded" ? "on-demand" : "provisioning";
    default: return null;
  }
}

/** Database state of each live Workspace in an App the user can administer,
 *  keyed by Workspace ID. Never includes provider names, URLs or tokens. */
export async function listWorkspaceDatabaseStates(
  userID: string,
  appID: string,
  client: SQLClient = controlDatabase(),
): Promise<Record<string, WorkspaceDatabaseState>> {
  const { rows } = await client.query<{ id: string; status: string | null; registration_status: string | null }>(`
    SELECT w.id, d.status, registration.status AS registration_status
    FROM cantelop_control.workspaces w
    JOIN cantelop_control.apps a ON a.id = w.app_id
    LEFT JOIN cantelop_control.workspace_databases d ON d.workspace_id = w.id
    ${latestRegistrationSQL}
    WHERE w.app_id = $1 AND w.archived_at IS NULL AND a.status <> 'archived'
      AND ${appAccessibleSQL()}
  `, [appID, userID]);
  const states: Record<string, WorkspaceDatabaseState> = {};
  for (const row of rows) {
    const state = workspaceDatabaseState(row.status, row.registration_status);
    if (state) states[row.id] = state;
  }
  return states;
}
