import { controlDatabase, type SQLClient } from "./database";
import { latestRegistrationSQL, workspaceDatabaseState, type WorkspaceDatabaseState } from "./workspace-database-states";

export interface AdminWorkspace {
  id: string;
  slug: string;
  app_id: string;
  app_slug: string;
  // "unpurged": archived (e.g. with its App, before purges were queued on App
  // archive) but no purge was ever queued, so its data is still retained.
  status: "active" | "deleting" | "deletion-failed" | "unpurged";
  database: WorkspaceDatabaseState | null;
  database_name: string | null;
  created_at: string;
  archived_at: string | null;
}

// Every live workspace and unfinished Workspace deletion on the platform,
// newest first, with the App it belongs to. Successfully purged Workspaces
// remain as audit tombstones in the control database but are not inventory.
export async function listAdminWorkspaces(client: SQLClient = controlDatabase()): Promise<AdminWorkspace[]> {
  const { rows } = await client.query<{
    id: string;
    slug: string;
    app_id: string;
    app_slug: string;
    created_at: Date | string;
    archived_at: Date | string | null;
    purge_status: string | null;
    database_status: string | null;
    database_name: string | null;
    registration_status: string | null;
  }>(`
    SELECT w.id, w.slug, w.app_id, a.slug AS app_slug, w.created_at, w.archived_at,
      purge.status AS purge_status, d.status AS database_status, d.provider_name AS database_name,
      registration.status AS registration_status
    FROM cantelop_control.workspaces w
    JOIN cantelop_control.apps a ON a.id = w.app_id
    LEFT JOIN cantelop_control.operations purge
      ON purge.owner_user_id = a.owner_user_id
     AND purge.kind = 'purge_workspace'
     AND purge.idempotency_key = 'purge-workspace:' || w.id
    LEFT JOIN cantelop_control.workspace_databases d ON d.workspace_id = w.id
    ${latestRegistrationSQL}
    WHERE w.archived_at IS NULL OR purge.status IS DISTINCT FROM 'succeeded'
    ORDER BY w.created_at DESC, w.id DESC
  `);
  return rows.filter((row) => row.archived_at === null || row.purge_status !== "succeeded").map((row) => ({
    id: row.id,
    slug: row.slug,
    app_id: row.app_id,
    app_slug: row.app_slug,
    status: row.archived_at === null ? "active"
      : row.purge_status === null ? "unpurged"
      : row.purge_status === "failed" ? "deletion-failed" : "deleting",
    database: workspaceDatabaseState(row.database_status, row.registration_status),
    database_name: row.database_name,
    created_at: new Date(row.created_at).toISOString(),
    archived_at: row.archived_at === null ? null : new Date(row.archived_at).toISOString(),
  }));
}
