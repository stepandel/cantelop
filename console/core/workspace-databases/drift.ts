import type { SQLClient } from "../db/database";
import type { WorkspaceDatabaseProvider } from "./provider";

export interface MissingDatabase { workspace_id: string; workspace_slug: string; app_slug: string; database_name: string }
export interface OrphanedDatabase { database_name: string; workspace_id: string | null; workspace_slug: string | null; app_slug: string | null }
export interface DatabaseDrift {
  checked_at: string;
  provider_databases: number;
  // Ready in the control database but absent from Turso, e.g. deleted from
  // the Turso dashboard. Credential requests for these Workspaces fail.
  missing: MissingDatabase[];
  // Present in Turso under this environment's prefix with no record, or with a
  // tombstone. Retained data nobody can reach or will ever delete.
  orphaned: OrphanedDatabase[];
}

/** Compares this environment's Turso databases with the control database.
 *  Read-only: an operator decides whether to recreate, purge or delete. */
export async function checkWorkspaceDatabaseDrift(
  client: SQLClient,
  provider: Pick<WorkspaceDatabaseProvider, "list">,
  environment: string,
  now: () => Date = () => new Date(),
): Promise<DatabaseDrift> {
  if (!/^[a-z0-9]{1,16}$/.test(environment)) throw new TypeError("invalid Turso environment");
  const prefix = `ct-${environment}-`;
  // Names are committed with the environment at reservation; records under
  // another prefix belong to a different deployment and group.
  // Read the provider first: a database provisioned between the two reads then
  // already has its record and cannot be reported as orphaned.
  const providerNames = new Set((await provider.list()).filter((name) => name.startsWith(prefix)));
  const { rows } = await client.query<{ workspace_id: string; workspace_slug: string; app_slug: string; status: string; provider_name: string }>(`
    SELECT d.workspace_id, w.slug AS workspace_slug, a.slug AS app_slug, d.status, d.provider_name
    FROM cantelop_control.workspace_databases d
    JOIN cantelop_control.workspaces w ON w.id = d.workspace_id
    JOIN cantelop_control.apps a ON a.id = w.app_id
    WHERE d.provider_name LIKE $1
  `, [`${prefix}%`]);
  const records = new Map(rows.map((row) => [row.provider_name, row]));
  const missing = rows
    .filter((row) => row.status === "ready" && !providerNames.has(row.provider_name))
    .map((row) => ({ workspace_id: row.workspace_id, workspace_slug: row.workspace_slug, app_slug: row.app_slug, database_name: row.provider_name }));
  const orphaned: OrphanedDatabase[] = [];
  for (const name of [...providerNames].sort()) {
    const row = records.get(name);
    // Pending and deleting records are provisioning or purging right now.
    if (row && row.status !== "deleted") continue;
    orphaned.push({ database_name: name, workspace_id: row?.workspace_id ?? null, workspace_slug: row?.workspace_slug ?? null, app_slug: row?.app_slug ?? null });
  }
  return { checked_at: now().toISOString(), provider_databases: providerNames.size, missing, orphaned };
}
